// Copyright 2016 The prometheus-operator Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package framework

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"k8s.io/client-go/kubernetes"
	v1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/pkg/apis/extensions/v1beta1"
	"k8s.io/client-go/pkg/fields"
	"k8s.io/client-go/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/coreos/prometheus-operator/pkg/client/monitoring/v1alpha1"
	"github.com/coreos/prometheus-operator/pkg/k8sutil"
)

type Framework struct {
	KubeClient  kubernetes.Interface
	MonClient   *v1alpha1.MonitoringV1alpha1Client
	HTTPClient  *http.Client
	MasterHost  string
	Namespace   *v1.Namespace
	OperatorPod *v1.Pod
}

// Setup setups a test framework and returns it.
func New(ns, kubeconfig, opImage string) (*Framework, error) {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}

	cli, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	httpc := cli.CoreV1().RESTClient().(*rest.RESTClient).Client
	if err != nil {
		return nil, err
	}

	mclient, err := v1alpha1.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	namespace, err := cli.Core().Namespaces().Create(&v1.Namespace{
		ObjectMeta: v1.ObjectMeta{
			Name: ns,
		},
	})
	if err != nil {
		return nil, err
	}

	f := &Framework{
		MasterHost: config.Host,
		KubeClient: cli,
		MonClient:  mclient,
		HTTPClient: httpc,
		Namespace:  namespace,
	}

	err = f.setup(opImage)
	if err != nil {
		return nil, err
	}

	return f, nil
}

func (f *Framework) setup(opImage string) error {
	if err := f.setupPrometheusOperator(opImage); err != nil {
		return err
	}
	return nil
}

func (f *Framework) setupPrometheusOperator(opImage string) error {
	fn, err := filepath.Abs("../../deployment.yaml")
	if err != nil {
		return err
	}

	deployManifest, err := os.Open(fn)
	if err != nil {
		return err
	}

	deploy := v1beta1.Deployment{}
	err = yaml.NewYAMLOrJSONDecoder(deployManifest, 100).Decode(&deploy)
	if err != nil {
		return err
	}
	if opImage != "" {
		// Override operator image used, if specified when running tests.
		deploy.Spec.Template.Spec.Containers[0].Image = opImage
	}

	err = f.createDeployment(&deploy)
	if err != nil {
		return err
	}

	opts := v1.ListOptions{LabelSelector: fields.SelectorFromSet(fields.Set(deploy.Spec.Template.ObjectMeta.Labels)).String()}
	pl, err := f.WaitForPodsReady(60*time.Second, 1, opts)
	if err != nil {
		return err
	}
	f.OperatorPod = &pl.Items[0]

	err = k8sutil.WaitForTPRReady(f.KubeClient.Core().RESTClient(), v1alpha1.TPRGroup, v1alpha1.TPRVersion, v1alpha1.TPRPrometheusName)
	if err != nil {
		return err
	}

	err = k8sutil.WaitForTPRReady(f.KubeClient.Core().RESTClient(), v1alpha1.TPRGroup, v1alpha1.TPRVersion, v1alpha1.TPRServiceMonitorName)
	if err != nil {
		return err
	}

	return k8sutil.WaitForTPRReady(f.KubeClient.Core().RESTClient(), v1alpha1.TPRGroup, v1alpha1.TPRVersion, v1alpha1.TPRAlertmanagerName)
}

// Teardown tears down a previously initialized test environment.
func (f *Framework) Teardown() error {
	if err := f.KubeClient.Core().Namespaces().Delete(f.Namespace.Name, nil); err != nil {
		return err
	}

	return nil
}

// WaitForPodsReady waits for a selection of Pods to be running and each
// container to pass its readiness check.
func (f *Framework) WaitForPodsReady(timeout time.Duration, expectedReplicas int, opts v1.ListOptions) (*v1.PodList, error) {
	return waitForPodsReady(f.KubeClient.Core(), timeout, expectedReplicas, f.Namespace.Name, opts)
}

func waitForPodsReady(client v1client.CoreV1Interface, timeout time.Duration, expectedRunning int, namespace string, opts v1.ListOptions) (*v1.PodList, error) {
	t := time.After(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-t:
			return nil, fmt.Errorf("timed out while waiting for %d pod to be running", expectedRunning)
		case <-ticker.C:
			pl, err := client.Pods(namespace).List(opts)
			if err != nil {
				return nil, err
			}

			runningAndReady := 0
			if len(pl.Items) >= 0 {
				for _, p := range pl.Items {
					isRunningAndReady, err := k8sutil.PodRunningAndReady(p)
					if err != nil {
						return nil, err
					}
					if isRunningAndReady {
						runningAndReady++
					}
				}
				if runningAndReady == expectedRunning {
					return pl, nil
				}
			}
		}
	}
}

func (f *Framework) CreateDeployment(kclient kubernetes.Interface, ns string, deploy *v1beta1.Deployment) error {
	if _, err := f.KubeClient.Extensions().Deployments(ns).Create(deploy); err != nil {
		return err
	}

	return nil
}

func (f *Framework) createDeployment(deploy *v1beta1.Deployment) error {
	if _, err := f.KubeClient.Extensions().Deployments(f.Namespace.Name).Create(deploy); err != nil {
		return err
	}

	return nil
}

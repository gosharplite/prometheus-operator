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

package alertmanager

import (
	"fmt"
	"net/url"
	"path"

	"k8s.io/client-go/pkg/api/resource"
	"k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/pkg/apis/apps/v1beta1"
	"k8s.io/client-go/pkg/util/intstr"

	"github.com/coreos/prometheus-operator/pkg/client/monitoring/v1alpha1"
)

func makeStatefulSet(am *v1alpha1.Alertmanager, old *v1beta1.StatefulSet) *v1beta1.StatefulSet {
	// TODO(fabxc): is this the right point to inject defaults?
	// Ideally we would do it before storing but that's currently not possible.
	// Potentially an update handler on first insertion.

	if am.Spec.BaseImage == "" {
		am.Spec.BaseImage = "quay.io/prometheus/alertmanager"
	}
	if am.Spec.Version == "" {
		am.Spec.Version = "v0.5.1"
	}
	if am.Spec.Replicas < 1 {
		am.Spec.Replicas = 1
	}

	statefulset := &v1beta1.StatefulSet{
		ObjectMeta: v1.ObjectMeta{
			Name: am.Name,
		},
		Spec: makeStatefulSetSpec(am),
	}
	if vc := am.Spec.Storage; vc == nil {
		statefulset.Spec.Template.Spec.Volumes = append(statefulset.Spec.Template.Spec.Volumes, v1.Volume{
			Name: fmt.Sprintf("%s-db", am.Name),
			VolumeSource: v1.VolumeSource{
				EmptyDir: &v1.EmptyDirVolumeSource{},
			},
		})
	} else {
		pvc := v1.PersistentVolumeClaim{
			ObjectMeta: v1.ObjectMeta{
				Name: fmt.Sprintf("%s-db", am.Name),
			},
			Spec: v1.PersistentVolumeClaimSpec{
				AccessModes: []v1.PersistentVolumeAccessMode{v1.ReadWriteOnce},
				Resources:   vc.Resources,
				Selector:    vc.Selector,
			},
		}
		if len(vc.Class) > 0 {
			pvc.ObjectMeta.Annotations = map[string]string{
				"volume.beta.kubernetes.io/storage-class": vc.Class,
			}
		}
		statefulset.Spec.VolumeClaimTemplates = append(statefulset.Spec.VolumeClaimTemplates, pvc)
	}

	if old != nil {
		statefulset.Annotations = old.Annotations
	}
	return statefulset
}

func makeStatefulSetService(p *v1alpha1.Alertmanager) *v1.Service {
	svc := &v1.Service{
		ObjectMeta: v1.ObjectMeta{
			Name: "alertmanager",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "None",
			Ports: []v1.ServicePort{
				{
					Name:       "web",
					Port:       9093,
					TargetPort: intstr.FromInt(9093),
					Protocol:   v1.ProtocolTCP,
				},
				{
					Name:       "mesh",
					Port:       6783,
					TargetPort: intstr.FromInt(6783),
					Protocol:   v1.ProtocolTCP,
				},
			},
			Selector: map[string]string{
				"app": "alertmanager",
			},
		},
	}
	return svc
}

func makeStatefulSetSpec(a *v1alpha1.Alertmanager) v1beta1.StatefulSetSpec {
	image := fmt.Sprintf("%s:%s", a.Spec.BaseImage, a.Spec.Version)

	commands := []string{
		"/bin/alertmanager",
		fmt.Sprintf("-config.file=%s", "/etc/alertmanager/config/alertmanager.yaml"),
		fmt.Sprintf("-web.listen-address=:%d", 9093),
		fmt.Sprintf("-mesh.listen-address=:%d", 6783),
		fmt.Sprintf("-storage.path=%s", "/etc/alertmanager/data"),
	}

	webRoutePrefix := ""
	if a.Spec.ExternalURL != "" {
		commands = append(commands, "-web.external-url="+a.Spec.ExternalURL)
		extUrl, err := url.Parse(a.Spec.ExternalURL)
		if err == nil {
			webRoutePrefix = extUrl.Path
		}
	}

	localReloadURL := &url.URL{
		Scheme: "http",
		Host:   "localhost:9093",
		Path:   path.Clean(webRoutePrefix + "/-/reload"),
	}

	for i := int32(0); i < a.Spec.Replicas; i++ {
		commands = append(commands, fmt.Sprintf("-mesh.peer=%s-%d.%s.%s.svc", a.Name, i, "alertmanager", a.Namespace))
	}

	terminationGracePeriod := int64(0)
	return v1beta1.StatefulSetSpec{
		ServiceName: "alertmanager",
		Replicas:    &a.Spec.Replicas,
		Template: v1.PodTemplateSpec{
			ObjectMeta: v1.ObjectMeta{
				Labels: map[string]string{
					"app":          "alertmanager",
					"alertmanager": a.Name,
				},
			},
			Spec: v1.PodSpec{
				TerminationGracePeriodSeconds: &terminationGracePeriod,
				Containers: []v1.Container{
					{
						Command: commands,
						Name:    a.Name,
						Image:   image,
						Ports: []v1.ContainerPort{
							{
								Name:          "web",
								ContainerPort: 9093,
								Protocol:      v1.ProtocolTCP,
							},
							{
								Name:          "mesh",
								ContainerPort: 6783,
								Protocol:      v1.ProtocolTCP,
							},
						},
						VolumeMounts: []v1.VolumeMount{
							{
								Name:      "config-volume",
								MountPath: "/etc/alertmanager/config",
							},
							{
								Name:      fmt.Sprintf("%s-db", a.Name),
								MountPath: "/var/alertmanager/data",
								SubPath:   subPathForStorage(a.Spec.Storage),
							},
						},
					}, {
						Name:  "config-reloader",
						Image: "jimmidyson/configmap-reload",
						Args: []string{
							fmt.Sprintf("-webhook-url=%s", localReloadURL),
							"-volume-dir=/etc/alertmanager/config",
						},
						VolumeMounts: []v1.VolumeMount{
							{
								Name:      "config-volume",
								ReadOnly:  true,
								MountPath: "/etc/alertmanager/config",
							},
						},
						Resources: v1.ResourceRequirements{
							Limits: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("5m"),
								v1.ResourceMemory: resource.MustParse("10Mi"),
							},
						},
					},
				},
				Volumes: []v1.Volume{
					{
						Name: "config-volume",
						VolumeSource: v1.VolumeSource{
							ConfigMap: &v1.ConfigMapVolumeSource{
								LocalObjectReference: v1.LocalObjectReference{
									Name: a.Name,
								},
							},
						},
					},
				},
			},
		},
	}
}

func subPathForStorage(s *v1alpha1.StorageSpec) string {
	if s == nil {
		return ""
	}

	return "alertmanager-db"
}

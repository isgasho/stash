package util

import (
	"strconv"

	rapi "github.com/appscode/stash/api"
	sapi "github.com/appscode/stash/api"
	scs "github.com/appscode/stash/client/clientset"
	"github.com/appscode/stash/pkg/docker"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	clientset "k8s.io/client-go/kubernetes"
	apiv1 "k8s.io/client-go/pkg/api/v1"
)

const (
	OperatorName   = "stash-operator"
	StashContainer = "stash"

	ScratchDirVolumeName = "stash-scratchdir"
	PodinfoVolumeName    = "stash-podinfo"
)

func IsPreferredAPIResource(kubeClient clientset.Interface, groupVersion, kind string) bool {
	if resourceList, err := kubeClient.Discovery().ServerPreferredResources(); err == nil {
		for _, resources := range resourceList {
			if resources.GroupVersion != groupVersion {
				continue
			}
			for _, resource := range resources.APIResources {
				if resources.GroupVersion == groupVersion && resource.Kind == kind {
					return true
				}
			}
		}
	}
	return false
}

func FindRestic(stashClient scs.ExtensionInterface, obj metav1.ObjectMeta) (*sapi.Restic, error) {
	restics, err := stashClient.Restics(obj.Namespace).List(metav1.ListOptions{LabelSelector: labels.Everything().String()})
	if kerr.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	for _, restic := range restics.Items {
		selector, err := metav1.LabelSelectorAsSelector(&restic.Spec.Selector)
		//return nil, fmt.Errorf("invalid selector: %v", err)
		if err == nil {
			if selector.Matches(labels.Set(obj.Labels)) {
				return &restic, nil
			}
		}
	}
	return nil, nil
}

func RestartPods(kubeClient clientset.Interface, namespace string, selector *metav1.LabelSelector) error {
	r, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return err
	}
	return kubeClient.CoreV1().Pods(namespace).DeleteCollection(&metav1.DeleteOptions{}, metav1.ListOptions{
		LabelSelector: r.String(),
	})
}

func GetString(m map[string]string, key string) string {
	if m == nil {
		return ""
	}
	return m[key]
}

func GetSidecarContainer(r *rapi.Restic, tag, app string, prefixHostname bool) apiv1.Container {
	if r.Annotations != nil {
		if v, ok := r.Annotations[sapi.VersionTag]; ok {
			tag = v
		}
	}

	sidecar := apiv1.Container{
		Name:            StashContainer,
		Image:           docker.ImageOperator + ":" + tag,
		ImagePullPolicy: apiv1.PullIfNotPresent,
		Args: []string{
			"schedule",
			"--v=3",
			"--app=" + app,
			"--namespace=" + r.Namespace,
			"--name=" + r.Name,
			"--prefixHostname=" + strconv.FormatBool(prefixHostname),
		},
		VolumeMounts: []apiv1.VolumeMount{
			{
				Name:      ScratchDirVolumeName,
				MountPath: "/tmp",
			},
		},
	}
	if r.Spec.Backend.Local != nil {
		sidecar.VolumeMounts = append(sidecar.VolumeMounts, apiv1.VolumeMount{
			Name:      r.Spec.Backend.Local.Volume.Name,
			MountPath: r.Spec.Backend.Local.Path,
		})
	}
	return sidecar
}

func AddAnnotation(r *rapi.Restic, tag string) {
	if r.ObjectMeta.Annotations == nil {
		r.ObjectMeta.Annotations = make(map[string]string)
	}
	r.ObjectMeta.Annotations[sapi.VersionTag] = tag
}

func RemoveContainer(c []apiv1.Container, name string) []apiv1.Container {
	for i, v := range c {
		if v.Name == name {
			c = append(c[:i], c[i+1:]...)
			break
		}
	}
	return c
}

func AddScratchVolume(volumes []apiv1.Volume) []apiv1.Volume {
	return append(volumes, apiv1.Volume{
		Name: ScratchDirVolumeName,
		VolumeSource: apiv1.VolumeSource{
			EmptyDir: &apiv1.EmptyDirVolumeSource{},
		},
	})
}

// https://kubernetes.io/docs/tasks/inject-data-application/downward-api-volume-expose-pod-information/#store-pod-fields
func AddDownwardVolume(volumes []apiv1.Volume) []apiv1.Volume {
	return append(volumes, apiv1.Volume{
		Name: PodinfoVolumeName,
		VolumeSource: apiv1.VolumeSource{
			DownwardAPI: &apiv1.DownwardAPIVolumeSource{
				Items: []apiv1.DownwardAPIVolumeFile{
					{
						Path: "labels",
						FieldRef: &apiv1.ObjectFieldSelector{
							FieldPath: "metadata.labels",
						},
					},
				},
			},
		},
	})
}

func RemoveVolume(volumes []apiv1.Volume, name string) []apiv1.Volume {
	for i, v := range volumes {
		if v.Name == name {
			volumes = append(volumes[:i], volumes[i+1:]...)
			break
		}
	}
	return volumes
}
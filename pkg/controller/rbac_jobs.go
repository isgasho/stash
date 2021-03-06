package controller

import (
	"fmt"
	"strings"

	"github.com/golang/glog"
	crdv1 "github.com/kubernetes-csi/external-snapshotter/pkg/apis/volumesnapshot/v1alpha1"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	policy "k8s.io/api/policy/v1beta1"
	rbac "k8s.io/api/rbac/v1"
	storage_api_v1 "k8s.io/api/storage/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/reference"
	core_util "kmodules.xyz/client-go/core/v1"
	meta_util "kmodules.xyz/client-go/meta"
	rbac_util "kmodules.xyz/client-go/rbac/v1"
	appCatalog "kmodules.xyz/custom-resources/apis/appcatalog/v1alpha1"
	api_v1alpha1 "stash.appscode.dev/stash/apis/stash/v1alpha1"
	api_v1beta1 "stash.appscode.dev/stash/apis/stash/v1beta1"
	stash_scheme "stash.appscode.dev/stash/client/clientset/versioned/scheme"
	"stash.appscode.dev/stash/pkg/util"
)

const (
	SidecarClusterRole               = "stash-sidecar"
	ScaledownJobRole                 = "stash-scaledownjob"
	RestoreInitContainerClusterRole  = "stash-restore-init-container"
	RestoreJobClusterRole            = "stash-restore-job"
	BackupJobClusterRole             = "stash-backup-job"
	VolumeSnapshotClusterRole        = "stash-volumesnapshot-job"
	VolumeSnapshotRestoreClusterRole = "stash-volumesnapshot-restore-job"
	CronJobClusterRole               = "stash-cron-job"
	KindRole                         = "Role"
	KindClusterRole                  = "ClusterRole"
	StorageClassClusterRole          = "stash-storageclass"
)

func (c *StashController) getBackupJobRoleBindingName(name string) string {
	return name + "-" + BackupJobClusterRole
}

func (c *StashController) getVolumesnapshotJobRoleBindingName(name string) string {
	return name + "-" + VolumeSnapshotClusterRole
}

func (c *StashController) getRestoreJobRoleBindingName(name string) string {
	return name + "-" + RestoreJobClusterRole
}

func (c *StashController) getVolumeSnapshotRestoreJobRoleBindingName(name string) string {
	return name + "-" + VolumeSnapshotRestoreClusterRole
}
func (c *StashController) getStorageClassClusterRoleBindingName(name string) string {
	return name + "-" + StorageClassClusterRole
}

func (c *StashController) ensureCronJobRBAC(resource *core.ObjectReference, sa string, psps []string, labels map[string]string) error {
	// ensure CronJob cluster role
	err := c.ensureCronJobClusterRole(psps, labels)
	if err != nil {
		return err
	}

	// ensure RoleBinding
	err = c.ensureCronJobRoleBinding(resource, sa, labels)
	return nil
}

func (c *StashController) ensureCronJobClusterRole(psps []string, labels map[string]string) error {
	meta := metav1.ObjectMeta{
		Name:   CronJobClusterRole,
		Labels: labels,
	}
	_, _, err := rbac_util.CreateOrPatchClusterRole(c.kubeClient, meta, func(in *rbac.ClusterRole) *rbac.ClusterRole {
		in.Rules = []rbac.PolicyRule{
			{
				APIGroups: []string{api_v1beta1.SchemeGroupVersion.Group},
				Resources: []string{api_v1beta1.ResourcePluralBackupSession},
				Verbs:     []string{"*"},
			},
			{
				APIGroups: []string{api_v1beta1.SchemeGroupVersion.Group},
				Resources: []string{api_v1beta1.ResourcePluralBackupConfiguration},
				Verbs:     []string{"*"},
			},
			{
				APIGroups: []string{core.GroupName},
				Resources: []string{"events"},
				Verbs:     []string{"create"},
			},
			{
				APIGroups:     []string{policy.GroupName},
				Resources:     []string{"podsecuritypolicies"},
				Verbs:         []string{"use"},
				ResourceNames: psps,
			},
			{
				APIGroups: []string{apps.GroupName},
				Resources: []string{"deployments", "statefulsets", "replicasets", "daemonsets"},
				Verbs:     []string{"get"},
			},
			{
				APIGroups: []string{core.GroupName},
				Resources: []string{"replicationcontrollers", "persistentvolumeclaims"},
				Verbs:     []string{"get"},
			},
			{
				APIGroups: []string{"apps.openshift.io"},
				Resources: []string{"deploymentconfigs"},
				Verbs:     []string{"get"},
			},
			{
				APIGroups: []string{"appcatalog.appscode.com"},
				Resources: []string{"*"},
				Verbs:     []string{"get"},
			},
		}
		return in

	})
	return err
}

func (c *StashController) ensureCronJobRoleBinding(resource *core.ObjectReference, sa string, labels map[string]string) error {
	meta := metav1.ObjectMeta{
		Name:      resource.Name,
		Namespace: resource.Namespace,
		Labels:    labels,
	}

	// ensure role binding
	_, _, err := rbac_util.CreateOrPatchRoleBinding(c.kubeClient, meta, func(in *rbac.RoleBinding) *rbac.RoleBinding {
		core_util.EnsureOwnerReference(&in.ObjectMeta, resource)

		in.RoleRef = rbac.RoleRef{
			APIGroup: rbac.GroupName,
			Kind:     KindClusterRole,
			Name:     CronJobClusterRole,
		}
		in.Subjects = []rbac.Subject{
			{
				Kind:      rbac.ServiceAccountKind,
				Name:      sa,
				Namespace: resource.Namespace,
			},
		}
		return in
	})
	if err != nil {
		return err
	}
	return nil
}

// use scaledownjob-role, service-account and role-binding name same as job name
// set job as owner of role, service-account and role-binding
func (c *StashController) ensureScaledownJobRBAC(resource *core.ObjectReference) error {
	// ensure roles
	meta := metav1.ObjectMeta{
		Name:      ScaledownJobRole,
		Namespace: resource.Namespace,
	}
	_, _, err := rbac_util.CreateOrPatchRole(c.kubeClient, meta, func(in *rbac.Role) *rbac.Role {
		core_util.EnsureOwnerReference(&in.ObjectMeta, resource)

		if in.Labels == nil {
			in.Labels = map[string]string{}
		}
		in.Labels["app"] = "stash"

		in.Rules = []rbac.PolicyRule{
			{
				APIGroups: []string{core.GroupName},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list", "delete", "deletecollection"},
			},
			{
				APIGroups: []string{apps.GroupName},
				Resources: []string{"deployments", "statefulsets"},
				Verbs:     []string{"get", "list", "patch"},
			},
			{
				APIGroups: []string{apps.GroupName},
				Resources: []string{"daemonsets", "replicasets"},
				Verbs:     []string{"get", "list", "patch"},
			},
			{
				APIGroups: []string{core.GroupName},
				Resources: []string{"replicationcontrollers"},
				Verbs:     []string{"get", "list", "patch"},
			},
		}
		return in
	})
	if err != nil {
		return err
	}

	// ensure service account
	meta = metav1.ObjectMeta{
		Name:      resource.Name,
		Namespace: resource.Namespace,
	}
	_, _, err = core_util.CreateOrPatchServiceAccount(c.kubeClient, meta, func(in *core.ServiceAccount) *core.ServiceAccount {
		core_util.EnsureOwnerReference(&in.ObjectMeta, resource)
		if in.Labels == nil {
			in.Labels = map[string]string{}
		}
		in.Labels["app"] = "stash"
		return in
	})
	if err != nil {
		return err
	}

	// ensure role binding
	_, _, err = rbac_util.CreateOrPatchRoleBinding(c.kubeClient, meta, func(in *rbac.RoleBinding) *rbac.RoleBinding {
		core_util.EnsureOwnerReference(&in.ObjectMeta, resource)

		if in.Labels == nil {
			in.Labels = map[string]string{}
		}
		in.Labels["app"] = "stash"

		in.RoleRef = rbac.RoleRef{
			APIGroup: rbac.GroupName,
			Kind:     "Role",
			Name:     ScaledownJobRole,
		}
		in.Subjects = []rbac.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      meta.Name,
				Namespace: meta.Namespace,
			},
		}
		return in
	})
	return err
}

// use sidecar-cluster-role, service-account and role-binding name same as job name
// set job as owner of service-account and role-binding
func (c *StashController) ensureRecoveryRBAC(resource *core.ObjectReference) error {
	// ensure service account
	meta := metav1.ObjectMeta{
		Name:      resource.Name,
		Namespace: resource.Namespace,
	}
	_, _, err := core_util.CreateOrPatchServiceAccount(c.kubeClient, meta, func(in *core.ServiceAccount) *core.ServiceAccount {
		core_util.EnsureOwnerReference(&in.ObjectMeta, resource)
		if in.Labels == nil {
			in.Labels = map[string]string{}
		}
		return in
	})
	if err != nil {
		return err
	}

	// ensure role binding
	_, _, err = rbac_util.CreateOrPatchRoleBinding(c.kubeClient, meta, func(in *rbac.RoleBinding) *rbac.RoleBinding {
		core_util.EnsureOwnerReference(&in.ObjectMeta, resource)

		if in.Labels == nil {
			in.Labels = map[string]string{}
		}
		in.Labels["app"] = "stash"

		in.RoleRef = rbac.RoleRef{
			APIGroup: rbac.GroupName,
			Kind:     "ClusterRole",
			Name:     SidecarClusterRole,
		}
		in.Subjects = []rbac.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      meta.Name,
				Namespace: meta.Namespace,
			},
		}
		return in
	})
	return err
}

func getRepoReaderRoleName(repoName string) string {
	return "appscode:stash:repo-reader:" + repoName
}

func GetRepoReaderRoleBindingName(name, namespace string) string {
	return name + ":" + namespace + ":repo-reader"
}

func (c *StashController) ensureRepoReaderRole(repo *api_v1alpha1.Repository) error {
	meta := metav1.ObjectMeta{
		Name:      getRepoReaderRoleName(repo.Name),
		Namespace: repo.Namespace,
	}

	ref, err := reference.GetReference(stash_scheme.Scheme, repo)
	if err != nil {
		return err
	}
	_, _, err = rbac_util.CreateOrPatchRole(c.kubeClient, meta, func(in *rbac.Role) *rbac.Role {
		core_util.EnsureOwnerReference(&in.ObjectMeta, ref)

		if in.Labels == nil {
			in.Labels = map[string]string{}
		}
		in.Labels["app"] = "stash"

		in.Rules = []rbac.PolicyRule{
			{
				APIGroups:     []string{api.SchemeGroupVersion.Group},
				Resources:     []string{"repositories"},
				ResourceNames: []string{repo.Name},
				Verbs:         []string{"get"},
			},
			{
				APIGroups:     []string{core.GroupName},
				Resources:     []string{"secrets"},
				ResourceNames: []string{repo.Spec.Backend.StorageSecretName},
				Verbs:         []string{"get"},
			},
		}

		return in
	})
	return err
}

func (c *StashController) ensureRepoReaderRBAC(resource *core.ObjectReference, rec *api_v1alpha1.Recovery) error {
	meta := metav1.ObjectMeta{
		Name:      GetRepoReaderRoleBindingName(resource.Name, resource.Namespace),
		Namespace: rec.Spec.Repository.Namespace,
	}

	repo, err := c.stashClient.StashV1alpha1().Repositories(rec.Spec.Repository.Namespace).Get(rec.Spec.Repository.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// ensure repo-reader role
	err = c.ensureRepoReaderRole(repo)
	if err != nil {
		return err
	}

	// ensure repo-reader role binding
	_, _, err = rbac_util.CreateOrPatchRoleBinding(c.kubeClient, meta, func(in *rbac.RoleBinding) *rbac.RoleBinding {

		if in.Labels == nil {
			in.Labels = map[string]string{}
		}
		in.Labels["app"] = "stash"

		in.RoleRef = rbac.RoleRef{
			APIGroup: rbac.GroupName,
			Kind:     "Role",
			Name:     getRepoReaderRoleName(rec.Spec.Repository.Name),
		}

		in.Subjects = []rbac.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      resource.Name,
				Namespace: resource.Namespace,
			},
		}
		return in
	})
	return err
}

func (c *StashController) ensureRepoReaderRolebindingDeleted(meta *metav1.ObjectMeta) error {
	// if the job is not recovery job then don't do anything
	if !strings.HasPrefix(meta.Name, util.RecoveryJobPrefix) {
		return nil
	}

	// read recovery name from label
	if !meta_util.HasKey(meta.Labels, util.AnnotationRecovery) {
		return fmt.Errorf("missing recovery name in job's label")
	}

	recoveryName, err := meta_util.GetStringValue(meta.Labels, util.AnnotationRecovery)
	if err != nil {
		return err
	}

	// read recovery object
	recovery, err := c.stashClient.StashV1alpha1().Recoveries(meta.Namespace).Get(recoveryName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// delete role binding
	err = c.kubeClient.RbacV1().RoleBindings(recovery.Spec.Repository.Namespace).Delete(GetRepoReaderRoleBindingName(meta.Name, meta.Namespace), meta_util.DeleteInBackground())
	if err != nil && !kerr.IsNotFound(err) {
		return err
	}
	glog.Infof("Deleted repo-reader rolebinding: " + GetRepoReaderRoleBindingName(meta.Name, meta.Namespace))
	return nil
}

func (c *StashController) ensureRestoreJobRBAC(ref *core.ObjectReference, sa string, psps []string, labels map[string]string) error {
	// ensure ClusterRole for restore job
	err := c.ensureRestoreJobClusterRole(psps, labels)
	if err != nil {
		return err
	}

	// ensure RoleBinding for restore job
	err = c.ensureRestoreJobRoleBinding(ref, sa, labels)
	if err != nil {
		return err
	}

	return nil
}

func (c *StashController) ensureRestoreJobClusterRole(psps []string, labels map[string]string) error {

	meta := metav1.ObjectMeta{
		Name:   RestoreJobClusterRole,
		Labels: labels,
	}
	_, _, err := rbac_util.CreateOrPatchClusterRole(c.kubeClient, meta, func(in *rbac.ClusterRole) *rbac.ClusterRole {

		in.Rules = []rbac.PolicyRule{
			{
				APIGroups: []string{api_v1beta1.SchemeGroupVersion.Group},
				Resources: []string{
					api_v1beta1.ResourcePluralRestoreSession,
					fmt.Sprintf("%s/status", api_v1beta1.ResourcePluralRestoreSession)},
				Verbs: []string{"*"},
			},
			{
				APIGroups: []string{core.GroupName},
				Resources: []string{"events"},
				Verbs:     []string{"create"},
			},
			{
				APIGroups:     []string{policy.GroupName},
				Resources:     []string{"podsecuritypolicies"},
				Verbs:         []string{"use"},
				ResourceNames: psps,
			},
		}
		return in
	})
	return err
}

func (c *StashController) ensureRestoreJobRoleBinding(resource *core.ObjectReference, sa string, labels map[string]string) error {

	meta := metav1.ObjectMeta{
		Namespace: resource.Namespace,
		Name:      c.getRestoreJobRoleBindingName(resource.Name),
		Labels:    labels,
	}
	_, _, err := rbac_util.CreateOrPatchRoleBinding(c.kubeClient, meta, func(in *rbac.RoleBinding) *rbac.RoleBinding {
		core_util.EnsureOwnerReference(&in.ObjectMeta, resource)

		in.RoleRef = rbac.RoleRef{
			APIGroup: rbac.GroupName,
			Kind:     "ClusterRole",
			Name:     RestoreJobClusterRole,
		}
		in.Subjects = []rbac.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      sa,
				Namespace: resource.Namespace,
			},
		}
		return in
	})
	return err
}

func (c *StashController) ensureVolumeSnapshotJobRBAC(ref *core.ObjectReference, sa string, labels map[string]string) error {
	// ensure ClusterRole for VolumeSnapshot job
	err := c.ensureVolumeSnapshotJobClusterRole(labels)
	if err != nil {
		return err
	}

	// ensure RoleBinding for VolumeSnapshot job
	err = c.ensureVolumeSnapshotJobRoleBinding(ref, sa, labels)
	if err != nil {
		return err
	}

	return nil
}

func (c *StashController) ensureBackupJobRBAC(ref *core.ObjectReference, sa string, psps []string, labels map[string]string) error {
	// ensure ClusterRole for restore job
	err := c.ensureBackupJobClusterRole(psps, labels)
	if err != nil {
		return err
	}

	// ensure RoleBinding for restore job
	err = c.ensureBackupJobRoleBinding(ref, sa, labels)
	if err != nil {
		return err
	}

	return nil
}

func (c *StashController) ensureBackupJobClusterRole(psps []string, labels map[string]string) error {

	meta := metav1.ObjectMeta{
		Name:   BackupJobClusterRole,
		Labels: labels,
	}
	_, _, err := rbac_util.CreateOrPatchClusterRole(c.kubeClient, meta, func(in *rbac.ClusterRole) *rbac.ClusterRole {

		in.Rules = []rbac.PolicyRule{
			{
				APIGroups: []string{api_v1beta1.SchemeGroupVersion.Group},
				Resources: []string{"*"},
				Verbs:     []string{"*"},
			},
			{
				APIGroups: []string{api_v1alpha1.SchemeGroupVersion.Group},
				Resources: []string{"*"},
				Verbs:     []string{"*"},
			},
			{
				APIGroups: []string{appCatalog.SchemeGroupVersion.Group},
				Resources: []string{appCatalog.ResourceApps},
				Verbs:     []string{"get"},
			},
			{
				APIGroups: []string{core.SchemeGroupVersion.Group},
				Resources: []string{"secrets"},
				Verbs:     []string{"get"},
			},
			{
				APIGroups: []string{core.GroupName},
				Resources: []string{"events"},
				Verbs:     []string{"create"},
			},
			{
				APIGroups:     []string{policy.GroupName},
				Resources:     []string{"podsecuritypolicies"},
				Verbs:         []string{"use"},
				ResourceNames: psps,
			},
		}
		return in
	})
	return err
}

func (c *StashController) ensureBackupJobRoleBinding(resource *core.ObjectReference, sa string, labels map[string]string) error {

	meta := metav1.ObjectMeta{
		Namespace: resource.Namespace,
		Name:      c.getBackupJobRoleBindingName(resource.Name),
		Labels:    labels,
	}
	_, _, err := rbac_util.CreateOrPatchRoleBinding(c.kubeClient, meta, func(in *rbac.RoleBinding) *rbac.RoleBinding {
		core_util.EnsureOwnerReference(&in.ObjectMeta, resource)

		in.RoleRef = rbac.RoleRef{
			APIGroup: rbac.GroupName,
			Kind:     "ClusterRole",
			Name:     BackupJobClusterRole,
		}
		in.Subjects = []rbac.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      sa,
				Namespace: resource.Namespace,
			},
		}
		return in
	})
	return err
}

func (c *StashController) ensureVolumeSnapshotJobClusterRole(labels map[string]string) error {

	meta := metav1.ObjectMeta{
		Name:   VolumeSnapshotClusterRole,
		Labels: labels,
	}
	_, _, err := rbac_util.CreateOrPatchClusterRole(c.kubeClient, meta, func(in *rbac.ClusterRole) *rbac.ClusterRole {

		in.Rules = []rbac.PolicyRule{
			{
				APIGroups: []string{api_v1beta1.SchemeGroupVersion.Group},
				Resources: []string{"*"},
				Verbs:     []string{"*"},
			},
			{
				APIGroups: []string{api_v1alpha1.SchemeGroupVersion.Group},
				Resources: []string{"*"},
				Verbs:     []string{"*"},
			},
			{
				APIGroups: []string{core.GroupName},
				Resources: []string{"events"},
				Verbs:     []string{"create"},
			},
			{
				APIGroups: []string{apps.GroupName},
				Resources: []string{"deployments", "statefulsets"},
				Verbs:     []string{"get", "list"},
			},
			{
				APIGroups: []string{apps.GroupName},
				Resources: []string{"daemonsets", "replicasets"},
				Verbs:     []string{"get", "list"},
			},
			{
				APIGroups: []string{core.GroupName},
				Resources: []string{"replicationcontrollers"},
				Verbs:     []string{"get", "list"},
			},
			{
				APIGroups: []string{crdv1.GroupName},
				Resources: []string{"volumesnapshots", "volumesnapshotcontents", "volumesnapshotclasses"},
				Verbs:     []string{"create", "get", "list", "watch", "patch"},
			},
		}
		return in
	})
	return err
}

func (c *StashController) ensureVolumeSnapshotJobRoleBinding(resource *core.ObjectReference, sa string, labels map[string]string) error {

	meta := metav1.ObjectMeta{
		Namespace: resource.Namespace,
		Name:      c.getVolumesnapshotJobRoleBindingName(resource.Name),
		Labels:    labels,
	}
	_, _, err := rbac_util.CreateOrPatchRoleBinding(c.kubeClient, meta, func(in *rbac.RoleBinding) *rbac.RoleBinding {
		core_util.EnsureOwnerReference(&in.ObjectMeta, resource)

		in.RoleRef = rbac.RoleRef{
			APIGroup: rbac.GroupName,
			Kind:     KindClusterRole,
			Name:     VolumeSnapshotClusterRole,
		}
		in.Subjects = []rbac.Subject{
			{
				Kind:      rbac.ServiceAccountKind,
				Name:      sa,
				Namespace: resource.Namespace,
			},
		}
		return in
	})
	return err
}

func (c *StashController) ensureVolumeSnapshotRestoreJobRBAC(ref *core.ObjectReference, sa string, labels map[string]string) error {
	// ensure ClusterRole for restore job
	err := c.ensureVolumeSnapshotRestoreJobClusterRole(labels)
	if err != nil {
		return err
	}

	// ensure RoleBinding for restore job
	err = c.ensureVolumeSnapshotRestoreJobRoleBinding(ref, sa, labels)
	if err != nil {
		return err
	}

	//ensure storageClass ClusterRole for restore job
	err = c.ensureStorageClassClusterRole(labels)
	if err != nil {
		return err
	}

	//ensure storageClass ClusterRoleBinding for restore job
	err = c.ensureStorageClassClusterRoleBinding(ref, sa, labels)
	if err != nil {
		return err
	}

	return nil
}

func (c *StashController) ensureVolumeSnapshotRestoreJobClusterRole(labels map[string]string) error {

	meta := metav1.ObjectMeta{
		Name:   VolumeSnapshotRestoreClusterRole,
		Labels: labels,
	}
	_, _, err := rbac_util.CreateOrPatchClusterRole(c.kubeClient, meta, func(in *rbac.ClusterRole) *rbac.ClusterRole {

		in.Rules = []rbac.PolicyRule{
			{
				APIGroups: []string{api_v1beta1.SchemeGroupVersion.Group},
				Resources: []string{"*"},
				Verbs:     []string{"*"},
			},
			{
				APIGroups: []string{core.GroupName},
				Resources: []string{"events"},
				Verbs:     []string{"create"},
			},
			{
				APIGroups: []string{core.GroupName},
				Resources: []string{"persistentvolumeclaims"},
				Verbs:     []string{"get", "list", "watch", "create", "patch"},
			},
			{
				APIGroups: []string{storage_api_v1.GroupName},
				Resources: []string{"storageclasses"},
				Verbs:     []string{"get"},
			},
		}
		return in

	})
	return err
}

func (c *StashController) ensureVolumeSnapshotRestoreJobRoleBinding(resource *core.ObjectReference, sa string, labels map[string]string) error {

	meta := metav1.ObjectMeta{
		Namespace: resource.Namespace,
		Name:      c.getVolumeSnapshotRestoreJobRoleBindingName(resource.Name),
		Labels:    labels,
	}
	_, _, err := rbac_util.CreateOrPatchRoleBinding(c.kubeClient, meta, func(in *rbac.RoleBinding) *rbac.RoleBinding {
		core_util.EnsureOwnerReference(&in.ObjectMeta, resource)

		in.RoleRef = rbac.RoleRef{
			APIGroup: rbac.GroupName,
			Kind:     "ClusterRole",
			Name:     VolumeSnapshotRestoreClusterRole,
		}
		in.Subjects = []rbac.Subject{
			{
				Kind:      rbac.ServiceAccountKind,
				Name:      sa,
				Namespace: resource.Namespace,
			},
		}
		return in
	})
	return err
}

func (c *StashController) ensureStorageClassClusterRole(labels map[string]string) error {

	meta := metav1.ObjectMeta{
		Name:   StorageClassClusterRole,
		Labels: labels,
	}
	_, _, err := rbac_util.CreateOrPatchClusterRole(c.kubeClient, meta, func(in *rbac.ClusterRole) *rbac.ClusterRole {

		in.Rules = []rbac.PolicyRule{
			{
				APIGroups: []string{storage_api_v1.GroupName},
				Resources: []string{"storageclasses"},
				Verbs:     []string{"get"},
			},
		}
		return in

	})
	return err
}

func (c *StashController) ensureStorageClassClusterRoleBinding(resource *core.ObjectReference, sa string, labels map[string]string) error {

	meta := metav1.ObjectMeta{
		Name:      c.getStorageClassClusterRoleBindingName(resource.Name),
		Namespace: resource.Namespace,
		Labels:    labels,
	}
	_, _, err := rbac_util.CreateOrPatchClusterRoleBinding(c.kubeClient, meta, func(in *rbac.ClusterRoleBinding) *rbac.ClusterRoleBinding {
		core_util.EnsureOwnerReference(&in.ObjectMeta, resource)

		in.RoleRef = rbac.RoleRef{
			APIGroup: rbac.GroupName,
			Kind:     "ClusterRole",
			Name:     StorageClassClusterRole,
		}
		in.Subjects = []rbac.Subject{
			{
				Kind:      rbac.ServiceAccountKind,
				Name:      sa,
				Namespace: resource.Namespace,
			},
		}
		return in
	})
	return err
}

// SPDX-FileCopyrightText: Contributors to the Gardener project
//
// SPDX-License-Identifier: Apache-2.0

package operatingsystemconfig

import (
	"context"
	"errors"
	"fmt"

	extensionscontroller "github.com/gardener/gardener/extensions/pkg/controller"
	extensionswebhook "github.com/gardener/gardener/extensions/pkg/webhook"
	gcontext "github.com/gardener/gardener/extensions/pkg/webhook/context"
	"github.com/gardener/gardener/extensions/pkg/webhook/controlplane/genericmutator"
	v1beta1helper "github.com/gardener/gardener/pkg/api/core/v1beta1/helper"
	extensionsv1alpha1 "github.com/gardener/gardener/pkg/apis/extensions/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gardener/gardener-extension-shoot-rsyslog-relp/pkg/apis/rsyslog"
	"github.com/gardener/gardener-extension-shoot-rsyslog-relp/pkg/apis/rsyslog/validation"
)

// NewEnsurer creates a new controlplane ensurer.
func NewEnsurer(client client.Client, decoder runtime.Decoder, logger logr.Logger) genericmutator.Ensurer {
	return &ensurer{
		client:  client,
		decoder: decoder,
		logger:  logger.WithName("rsyslog-relp-ensurer"),
	}
}

type ensurer struct {
	genericmutator.NoopEnsurer
	client  client.Client
	decoder runtime.Decoder
	logger  logr.Logger
}

// EnsureAdditionalFiles ensures that the rsyslog configuration files are added to the <new> files.
func (e *ensurer) EnsureAdditionalFiles(ctx context.Context, gctx gcontext.GardenContext, newFiles, _ *[]extensionsv1alpha1.File) error {
	cluster, err := gctx.GetCluster(ctx)
	if err != nil {
		return err
	}

	if cluster.Shoot == nil {
		return errors.New("cluster.shoot is not yet populated")
	}

	if cluster.Shoot.DeletionTimestamp != nil {
		e.logger.Info("Shoot has a deletion timestamp set, skipping the OperatingSystemConfig mutation", "shoot", client.ObjectKeyFromObject(cluster.Shoot))
		return nil
	}

	extension := &extensionsv1alpha1.Extension{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shoot-rsyslog-relp",
			Namespace: cluster.ObjectMeta.Name,
		},
	}
	if err := e.client.Get(ctx, client.ObjectKeyFromObject(extension), extension); err != nil {
		return fmt.Errorf("failed to get extension '%s': %w", client.ObjectKeyFromObject(extension), err)
	}

	shootRsyslogRelpConfig := &rsyslog.RsyslogRelpConfig{}
	if extension.Spec.ProviderConfig != nil {
		if err := runtime.DecodeInto(e.decoder, extension.Spec.ProviderConfig.Raw, shootRsyslogRelpConfig); err != nil {
			return fmt.Errorf("failed to decode provider config: %w", err)
		}
	}

	rsyslogFiles, err := getRsyslogFiles(shootRsyslogRelpConfig, cluster)
	if err != nil {
		return fmt.Errorf("failed to get rsyslog files: %w", err)
	}
	for _, file := range rsyslogFiles {
		*newFiles = extensionswebhook.EnsureFileWithPath(*newFiles, file)
	}

	if shootRsyslogRelpConfig.TLS != nil && shootRsyslogRelpConfig.TLS.Enabled && shootRsyslogRelpConfig.TLS.SecretReferenceName != nil {
		if err := e.validateReferencedSecret(ctx, cluster, extension.Namespace, *shootRsyslogRelpConfig.TLS.SecretReferenceName); err != nil {
			return err
		}
	}

	if shootRsyslogRelpConfig.AuditConfig == nil || shootRsyslogRelpConfig.AuditConfig.Enabled {
		auditFiles, err := getAuditFiles(ctx, e.client, e.decoder, extension.Namespace, shootRsyslogRelpConfig, cluster)
		if err != nil {
			return fmt.Errorf("failed to get audit files: %w", err)
		}
		for _, file := range auditFiles {
			*newFiles = extensionswebhook.EnsureFileWithPath(*newFiles, file)
		}
	}

	return nil
}

func (e *ensurer) EnsureAdditionalUnits(_ context.Context, _ gcontext.GardenContext, newUnits, _ *[]extensionsv1alpha1.Unit) error {
	*newUnits = extensionswebhook.EnsureUnitWithName(*newUnits, getRsyslogConfiguratorUnit())
	return nil
}

// validateReferencedSecret re-validates the content of the rsyslog relp TLS secret referenced by the configuration.
// The secret is validated at Shoot admission time, but it can be mutated afterwards. Re-validating it here, as a
// defense-in-depth measure, ensures that invalid content never gets rendered into the Shoot nodes' files.
func (e *ensurer) validateReferencedSecret(ctx context.Context, cluster *extensionscontroller.Cluster, namespace, secretRefName string) error {
	ref := v1beta1helper.GetResourceByName(cluster.Shoot.Spec.Resources, secretRefName)
	if ref == nil || ref.ResourceRef.Kind != "Secret" {
		return fmt.Errorf("failed to find referenced resource with name %s and kind Secret", secretRefName)
	}

	secret := &corev1.Secret{}
	if err := extensionscontroller.GetObjectByReference(ctx, e.client, &ref.ResourceRef, namespace, secret); err != nil {
		return fmt.Errorf("failed to read referenced secret for reference %s: %w", secretRefName, err)
	}

	return validation.ValidateRsyslogRelpSecret(secret)
}

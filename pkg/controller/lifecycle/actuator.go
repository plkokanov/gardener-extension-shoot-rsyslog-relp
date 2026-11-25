// SPDX-FileCopyrightText: Contributors to the Gardener project
//
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"fmt"

	extensionscontroller "github.com/gardener/gardener/extensions/pkg/controller"
	"github.com/gardener/gardener/extensions/pkg/controller/extension"
	v1beta1helper "github.com/gardener/gardener/pkg/api/core/v1beta1/helper"
	extensionsv1alpha1 "github.com/gardener/gardener/pkg/apis/extensions/v1alpha1"
	"github.com/gardener/gardener/pkg/component"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gardener/gardener-extension-shoot-rsyslog-relp/imagevector"
	apisconfig "github.com/gardener/gardener-extension-shoot-rsyslog-relp/pkg/apis/config"
	api "github.com/gardener/gardener-extension-shoot-rsyslog-relp/pkg/apis/rsyslog"
	"github.com/gardener/gardener-extension-shoot-rsyslog-relp/pkg/apis/rsyslog/validation"
	"github.com/gardener/gardener-extension-shoot-rsyslog-relp/pkg/component/rsyslogrelpconfigcleaner"
	"github.com/gardener/gardener-extension-shoot-rsyslog-relp/pkg/constants"
)

// ActuatorName is the name of the rsyslog relp actuator.
const ActuatorName = constants.ServiceName + "-actuator"

// NewActuator returns an actuator responsible for Extension resources.
func NewActuator(client client.Client, decoder runtime.Decoder, config apisconfig.Configuration, chartRendererFactory extensionscontroller.ChartRendererFactory) extension.Actuator {
	return &actuator{
		client:               client,
		decoder:              decoder,
		config:               config,
		chartRendererFactory: chartRendererFactory,
	}
}

type actuator struct {
	chartRendererFactory extensionscontroller.ChartRendererFactory

	client  client.Client
	decoder runtime.Decoder
	config  apisconfig.Configuration
}

// Reconcile reconciles the extension resource.
func (a *actuator) Reconcile(ctx context.Context, _ logr.Logger, ex *extensionsv1alpha1.Extension) error {
	namespace := ex.GetNamespace()

	rsyslogRelpConfig := &api.RsyslogRelpConfig{}
	if err := runtime.DecodeInto(a.decoder, ex.Spec.ProviderConfig.Raw, rsyslogRelpConfig); err != nil {
		return fmt.Errorf("failed to decode provider config: %w", err)
	}

	cluster, err := extensionscontroller.GetCluster(ctx, a.client, namespace)
	if err != nil {
		return err
	}

	if cluster.Shoot == nil {
		return errors.New("cluster.shoot is not yet populated")
	}

	if err := a.validateReferencedResources(ctx, cluster, namespace, rsyslogRelpConfig); err != nil {
		return err
	}

	return deployMonitoringConfig(ctx, a.client, namespace, rsyslogRelpConfig.AuditConfig)
}

// validateReferencedResources re-validates the content of the resources referenced by the rsyslog relp configuration
// (the TLS secret and the audit configMap). These resources are validated at Shoot admission time, but they can be
// mutated afterwards. Re-validating them here ensures that invalid content never reaches the Shoot nodes and surfaces
// as a LastError on the Shoot status.
func (a *actuator) validateReferencedResources(ctx context.Context, cluster *extensionscontroller.Cluster, namespace string, rsyslogRelpConfig *api.RsyslogRelpConfig) error {
	if rsyslogRelpConfig.TLS != nil && rsyslogRelpConfig.TLS.Enabled && rsyslogRelpConfig.TLS.SecretReferenceName != nil {
		ref := v1beta1helper.GetResourceByName(cluster.Shoot.Spec.Resources, *rsyslogRelpConfig.TLS.SecretReferenceName)
		if ref == nil || ref.ResourceRef.Kind != "Secret" {
			return fmt.Errorf("failed to find referenced resource with name %s and kind Secret", *rsyslogRelpConfig.TLS.SecretReferenceName)
		}

		secret := &corev1.Secret{}
		if err := extensionscontroller.GetObjectByReference(ctx, a.client, &ref.ResourceRef, namespace, secret); err != nil {
			return fmt.Errorf("failed to read referenced secret for reference %s: %w", *rsyslogRelpConfig.TLS.SecretReferenceName, err)
		}

		if err := validation.ValidateRsyslogRelpSecret(secret); err != nil {
			return err
		}
	}

	if rsyslogRelpConfig.AuditConfig != nil && rsyslogRelpConfig.AuditConfig.Enabled && rsyslogRelpConfig.AuditConfig.ConfigMapReferenceName != nil {
		ref := v1beta1helper.GetResourceByName(cluster.Shoot.Spec.Resources, *rsyslogRelpConfig.AuditConfig.ConfigMapReferenceName)
		if ref == nil || ref.ResourceRef.Kind != "ConfigMap" {
			return fmt.Errorf("failed to find referenced resource with name %s and kind ConfigMap", *rsyslogRelpConfig.AuditConfig.ConfigMapReferenceName)
		}

		configMap := &corev1.ConfigMap{}
		if err := extensionscontroller.GetObjectByReference(ctx, a.client, &ref.ResourceRef, namespace, configMap); err != nil {
			return fmt.Errorf("failed to read referenced configMap for reference %s: %w", *rsyslogRelpConfig.AuditConfig.ConfigMapReferenceName, err)
		}

		if err := validation.ValidateAuditConfigMap(a.decoder, configMap); err != nil {
			return err
		}
	}

	return nil
}

// Delete deletes the extension resource.
func (a *actuator) Delete(ctx context.Context, _ logr.Logger, ex *extensionsv1alpha1.Extension) error {
	namespace := ex.GetNamespace()

	cluster, err := extensionscontroller.GetCluster(ctx, a.client, namespace)
	if err != nil {
		return err
	}

	if cluster.Shoot == nil {
		return errors.New("cluster.shoot is not yet populated")
	}

	if err := deleteMonitoringConfig(ctx, a.client, namespace); err != nil {
		return fmt.Errorf("failed cleaning up monitoring configuration: %w", err)
	}

	return cleanRsyslogRelpConfiguration(ctx, cluster, a.client, namespace)
}

// ForceDelete deletes the extension resource.
func (a *actuator) ForceDelete(_ context.Context, _ logr.Logger, _ *extensionsv1alpha1.Extension) error {
	return nil
}

// Restore restores the extension resource.
func (a *actuator) Restore(ctx context.Context, log logr.Logger, ex *extensionsv1alpha1.Extension) error {
	return a.Reconcile(ctx, log, ex)
}

// Migrate migrates the extension resource.
func (a *actuator) Migrate(_ context.Context, _ logr.Logger, _ *extensionsv1alpha1.Extension) error {
	return nil
}

func cleanRsyslogRelpConfiguration(ctx context.Context, cluster *extensionscontroller.Cluster, client client.Client, namespace string) error {
	// If the Shoot is hibernated, we don't have Nodes. Hence, there is no need to clean up anything.
	if extensionscontroller.IsHibernated(cluster) {
		return nil
	}

	alpineImage, err := imagevector.ImageVector().FindImage(imagevector.ImageNameAlpine)
	if err != nil {
		return fmt.Errorf("failed to find the alpine image: %w", err)
	}
	pauseImage, err := imagevector.ImageVector().FindImage(imagevector.ImageNamePauseContainer)
	if err != nil {
		return fmt.Errorf("failed to find the pause image: %w", err)
	}

	values := rsyslogrelpconfigcleaner.Values{
		AlpineImage:         alpineImage.String(),
		PauseContainerImage: pauseImage.String(),
	}
	cleaner := rsyslogrelpconfigcleaner.New(client, namespace, values)

	// If the Shoot is in deletion, then there is no need to deploy the component to clean up the rsyslog
	// configuration from Nodes. The Shoot deletion flow ensures that the Worker is deleted before
	// the Extension deletion. Hence, there are no Nodes, no need to deploy the configuration cleaner component.
	//
	// However, we should still try to destroy the
	// configuration cleaner component, in case it failed to be cleaned up in a previous reconciliation
	// where the extension was deleted before the shoot deletion was triggered.
	if cluster.Shoot.DeletionTimestamp == nil {
		if err := component.OpWait(cleaner).Deploy(ctx); err != nil {
			return fmt.Errorf("failed to deploy the rsyslog relp configuration cleaner component: %w", err)
		}
	}

	if err := component.OpDestroyAndWait(cleaner).Destroy(ctx); err != nil {
		return fmt.Errorf("failed to destroy the rsyslog relp configuration cleaner component: %w", err)
	}

	return nil
}

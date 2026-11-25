// SPDX-FileCopyrightText: Contributors to the Gardener project
//
// SPDX-License-Identifier: Apache-2.0

package lifecycle_test

import (
	"context"
	"encoding/json"

	"github.com/gardener/gardener/extensions/pkg/controller/extension"
	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	extensionsv1alpha1 "github.com/gardener/gardener/pkg/apis/extensions/v1alpha1"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	monitoringv1alpha1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1alpha1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	apisconfig "github.com/gardener/gardener-extension-shoot-rsyslog-relp/pkg/apis/config"
	"github.com/gardener/gardener-extension-shoot-rsyslog-relp/pkg/apis/rsyslog"
	rsysloginstall "github.com/gardener/gardener-extension-shoot-rsyslog-relp/pkg/apis/rsyslog/install"
	rsyslogv1alpha1 "github.com/gardener/gardener-extension-shoot-rsyslog-relp/pkg/apis/rsyslog/v1alpha1"
	"github.com/gardener/gardener-extension-shoot-rsyslog-relp/pkg/controller/lifecycle"
)

var _ = Describe("Actuator", func() {
	var (
		ctx = context.Background()

		decoder    runtime.Decoder
		fakeClient client.Client
		actuator   extension.Actuator

		namespace               = "shoot--foo--bar"
		extensionProviderConfig *rsyslog.RsyslogRelpConfig
		shoot                   *gardencorev1beta1.Shoot
		extensionResource       *extensionsv1alpha1.Extension
	)

	BeforeEach(func() {
		scheme := runtime.NewScheme()
		Expect(extensionsv1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(gardencorev1beta1.AddToScheme(scheme)).To(Succeed())
		Expect(rsysloginstall.AddToScheme(scheme)).To(Succeed())
		Expect(monitoringv1.AddToScheme(scheme)).To(Succeed())
		Expect(monitoringv1alpha1.AddToScheme(scheme)).To(Succeed())

		decoder = serializer.NewCodecFactory(scheme, serializer.EnableStrict).UniversalDecoder()
		fakeClient = fakeclient.NewClientBuilder().WithScheme(scheme).Build()

		actuator = lifecycle.NewActuator(fakeClient, decoder, apisconfig.Configuration{}, nil)

		extensionProviderConfig = &rsyslog.RsyslogRelpConfig{
			Target: "localhost",
			Port:   10250,
			TLS: &rsyslog.TLS{
				Enabled:             true,
				SecretReferenceName: new("rsyslog-tls"),
			},
			AuditConfig: &rsyslog.AuditConfig{
				Enabled:                true,
				ConfigMapReferenceName: new("audit-rules"),
			},
		}

		shoot = &gardencorev1beta1.Shoot{
			TypeMeta: metav1.TypeMeta{
				APIVersion: gardencorev1beta1.SchemeGroupVersion.String(),
				Kind:       "Shoot",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo",
				Namespace: "garden-bar",
			},
			Spec: gardencorev1beta1.ShootSpec{
				Resources: []gardencorev1beta1.NamedResourceReference{
					{
						Name: "rsyslog-tls",
						ResourceRef: autoscalingv1.CrossVersionObjectReference{
							Kind: "Secret",
							Name: "rsyslog-tls",
						},
					},
					{
						Name: "audit-rules",
						ResourceRef: autoscalingv1.CrossVersionObjectReference{
							Kind: "ConfigMap",
							Name: "audit-rules",
						},
					},
				},
			},
		}
	})

	JustBeforeEach(func() {
		shootRaw, err := json.Marshal(shoot)
		Expect(err).NotTo(HaveOccurred())

		cluster := &extensionsv1alpha1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
			Spec: extensionsv1alpha1.ClusterSpec{
				CloudProfile: runtime.RawExtension{Raw: []byte("{}")},
				Seed:         &runtime.RawExtension{Raw: []byte("{}")},
				Shoot:        runtime.RawExtension{Raw: shootRaw},
			},
		}
		Expect(fakeClient.Create(ctx, cluster)).To(Succeed())

		extensionResource = &extensionsv1alpha1.Extension{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shoot-rsyslog-relp",
				Namespace: namespace,
			},
			Spec: extensionsv1alpha1.ExtensionSpec{
				DefaultSpec: extensionsv1alpha1.DefaultSpec{
					ProviderConfig: &runtime.RawExtension{
						Raw: encodeProviderConfig(extensionProviderConfig),
					},
					Type: "shoot-rsyslog-relp",
				},
			},
		}
	})

	createRsyslogSecret := func(immutable bool, data map[string][]byte) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ref-rsyslog-tls",
				Namespace: namespace,
			},
			Immutable: new(immutable),
			Data:      data,
		}
		Expect(fakeClient.Create(ctx, secret)).To(Succeed())
	}

	createAuditConfigMap := func(immutable bool, data map[string]string) {
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ref-audit-rules",
				Namespace: namespace,
			},
			Immutable: new(immutable),
			Data:      data,
		}
		Expect(fakeClient.Create(ctx, configMap)).To(Succeed())
	}

	validSecretData := map[string][]byte{
		"ca":  []byte("ca"),
		"crt": []byte("crt"),
		"key": []byte("key"),
	}

	validConfigMapData := map[string]string{
		"auditd": `apiVersion: rsyslog-relp.extensions.gardener.cloud/v1alpha1
kind: Auditd
auditRules: |
  custom-rule-00`,
	}

	Describe("#Reconcile", func() {
		It("should succeed when the referenced secret and configMap contain valid content", func() {
			createRsyslogSecret(true, validSecretData)
			createAuditConfigMap(true, validConfigMapData)

			Expect(actuator.Reconcile(ctx, logr.Discard(), extensionResource)).To(Succeed())
		})

		It("should fail when the referenced secret is not immutable", func() {
			createRsyslogSecret(false, validSecretData)
			createAuditConfigMap(true, validConfigMapData)

			Expect(actuator.Reconcile(ctx, logr.Discard(), extensionResource)).To(MatchError(ContainSubstring("must be immutable")))
		})

		It("should fail when the referenced secret is missing a required data entry", func() {
			createRsyslogSecret(true, map[string][]byte{"ca": []byte("ca"), "crt": []byte("crt")})
			createAuditConfigMap(true, validConfigMapData)

			Expect(actuator.Reconcile(ctx, logr.Discard(), extensionResource)).To(MatchError(ContainSubstring("is missing key value")))
		})

		It("should fail when the referenced configMap is not immutable", func() {
			createRsyslogSecret(true, validSecretData)
			createAuditConfigMap(false, validConfigMapData)

			Expect(actuator.Reconcile(ctx, logr.Discard(), extensionResource)).To(MatchError(ContainSubstring("must be immutable")))
		})

		It("should fail when the referenced configMap contains audit config without any audit rules", func() {
			createRsyslogSecret(true, validSecretData)
			createAuditConfigMap(true, map[string]string{"auditd": `apiVersion: rsyslog-relp.extensions.gardener.cloud/v1alpha1
kind: Auditd`})

			Expect(actuator.Reconcile(ctx, logr.Discard(), extensionResource)).To(MatchError(ContainSubstring("auditRules must not be empty")))
		})

		It("should fail when the cluster's shoot is not yet populated", func() {
			emptyNamespace := "shoot--foo--empty"
			cluster := &extensionsv1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: emptyNamespace},
				Spec: extensionsv1alpha1.ClusterSpec{
					CloudProfile: runtime.RawExtension{Raw: []byte("{}")},
					Seed:         &runtime.RawExtension{Raw: []byte("{}")},
					Shoot:        runtime.RawExtension{Raw: []byte("null")},
				},
			}
			Expect(fakeClient.Create(ctx, cluster)).To(Succeed())

			ex := &extensionsv1alpha1.Extension{
				ObjectMeta: metav1.ObjectMeta{Name: "shoot-rsyslog-relp", Namespace: emptyNamespace},
				Spec:       extensionResource.Spec,
			}

			Expect(actuator.Reconcile(ctx, logr.Discard(), ex)).To(MatchError(ContainSubstring("cluster.shoot is not yet populated")))
		})
	})
})

func encodeProviderConfig(config *rsyslog.RsyslogRelpConfig) []byte {
	scheme := runtime.NewScheme()
	Expect(rsysloginstall.AddToScheme(scheme)).To(Succeed())

	codec := serializer.NewCodecFactory(scheme).LegacyCodec(rsyslogv1alpha1.SchemeGroupVersion)
	raw, err := runtime.Encode(codec, config)
	Expect(err).NotTo(HaveOccurred())

	return raw
}

// SPDX-FileCopyrightText: Contributors to the Gardener project
//
// SPDX-License-Identifier: Apache-2.0

package validation_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	rsysloginstall "github.com/gardener/gardener-extension-shoot-rsyslog-relp/pkg/apis/rsyslog/install"
	"github.com/gardener/gardener-extension-shoot-rsyslog-relp/pkg/apis/rsyslog/validation"
)

var _ = Describe("References", func() {
	Describe("#ValidateRsyslogRelpSecret", func() {
		validData := map[string][]byte{"ca": []byte("ca"), "crt": []byte("crt"), "key": []byte("key")}

		newSecret := func(immutable bool, data map[string][]byte) *corev1.Secret {
			return &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "rsyslog-tls", Namespace: "shoot--foo--bar"},
				Immutable:  new(immutable),
				Data:       data,
			}
		}

		It("should succeed when the secret is immutable and has the ca, crt and key entries", func() {
			Expect(validation.ValidateRsyslogRelpSecret(newSecret(true, validData))).To(Succeed())
		})

		DescribeTable("should reject the secret",
			func(secret *corev1.Secret, matcher gomegatypes.GomegaMatcher) {
				Expect(validation.ValidateRsyslogRelpSecret(secret)).To(matcher)
			},

			Entry("when the ca entry is missing",
				newSecret(true, map[string][]byte{"crt": []byte("crt"), "key": []byte("key")}),
				MatchError(ContainSubstring("is missing ca value")),
			),
			Entry("when the crt entry is missing",
				newSecret(true, map[string][]byte{"ca": []byte("ca"), "key": []byte("key")}),
				MatchError(ContainSubstring("is missing crt value")),
			),
			Entry("when the key entry is missing",
				newSecret(true, map[string][]byte{"ca": []byte("ca"), "crt": []byte("crt")}),
				MatchError(ContainSubstring("is missing key value")),
			),
			Entry("when the secret is not immutable",
				newSecret(false, validData),
				MatchError(ContainSubstring("must be immutable")),
			),
			Entry("when the secret has more than three entries",
				newSecret(true, map[string][]byte{"ca": []byte("ca"), "crt": []byte("crt"), "key": []byte("key"), "extra": []byte("extra")}),
				MatchError(ContainSubstring("should have only three data entries")),
			),
		)
	})

	Describe("#ValidateAuditConfigMap", func() {
		var decoder runtime.Decoder

		BeforeEach(func() {
			scheme := runtime.NewScheme()
			Expect(rsysloginstall.AddToScheme(scheme)).To(Succeed())
			decoder = serializer.NewCodecFactory(scheme, serializer.EnableStrict).UniversalDecoder()
		})

		validAuditd := `apiVersion: rsyslog-relp.extensions.gardener.cloud/v1alpha1
kind: Auditd
auditRules: |
  -a always,exit -F arch=b64 -S execve -k exec`

		newConfigMap := func(immutable bool, data map[string]string) *corev1.ConfigMap {
			return &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "audit-rules", Namespace: "shoot--foo--bar"},
				Immutable:  new(immutable),
				Data:       data,
			}
		}

		It("should succeed when the configMap is immutable and holds a valid auditd config", func() {
			Expect(validation.ValidateAuditConfigMap(decoder, newConfigMap(true, map[string]string{"auditd": validAuditd}))).To(Succeed())
		})

		DescribeTable("should reject the configMap",
			func(configMap *corev1.ConfigMap, matcher gomegatypes.GomegaMatcher) {
				Expect(validation.ValidateAuditConfigMap(decoder, configMap)).To(matcher)
			},

			Entry("when the configMap is not immutable",
				newConfigMap(false, map[string]string{"auditd": validAuditd}),
				MatchError(ContainSubstring("must be immutable")),
			),
			Entry("when the auditd entry is missing",
				newConfigMap(true, map[string]string{"other": validAuditd}),
				MatchError(ContainSubstring("missing 'data.auditd' field")),
			),
			Entry("when the configMap has more than one entry",
				newConfigMap(true, map[string]string{"auditd": validAuditd, "extra": "extra"}),
				MatchError(ContainSubstring("should have only one entry")),
			),
			Entry("when the auditd entry is empty",
				newConfigMap(true, map[string]string{"auditd": ""}),
				MatchError(ContainSubstring("empty auditd config")),
			),
			Entry("when the auditd config cannot be decoded",
				newConfigMap(true, map[string]string{"auditd": "not a valid auditd config"}),
				MatchError(ContainSubstring("could not decode 'data.auditd' field")),
			),
			Entry("when the auditd config has no audit rules",
				newConfigMap(true, map[string]string{"auditd": `apiVersion: rsyslog-relp.extensions.gardener.cloud/v1alpha1
kind: Auditd`}),
				MatchError(ContainSubstring("auditRules must not be empty")),
			),
		)
	})
})

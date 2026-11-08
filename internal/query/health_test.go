package query_test

import (
	"testing"
	"time"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/fake"
	"github.com/yindia/p9s/internal/engines/fake/fixtures"
	"github.com/yindia/p9s/internal/query"
)

// soon expires 2026-11-15 (7 days after fixtures.Now); later in 2036.
const soon = `-----BEGIN CERTIFICATE-----
MIIDNjCCAh6gAwIBAgIUCi5SXeTZidE9R570Od/40I9+b+owDQYJKoZIhvcNAQEL
BQAwOzE5MDcGA1UEAwwwZ2F0ZWtlZXBlci13ZWJob29rLXNlcnZpY2UuZ2F0ZWtl
ZXBlci1zeXN0ZW0uc3ZjMB4XDTI2MTEwODEyNTU0NloXDTI2MTExNTEyNTU0Nlow
OzE5MDcGA1UEAwwwZ2F0ZWtlZXBlci13ZWJob29rLXNlcnZpY2UuZ2F0ZWtlZXBl
ci1zeXN0ZW0uc3ZjMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA4WHy
u2690Dke2cAYAd0ZOjVsVjHOLuh6G6B0UAh1opR8p3A2Knp2UKGQujhZkr1h0ExL
/BEuMq8l9LYYR5l7qu/elk5meXo9F4EnWkPWoiht50d1RNGaD+Ue7Tks+uljEcHO
PRoj4/ynu30XW2mlzPeVMPGrZwNcfkVNqiX9VaaWkWoKU1JprOOudhhTVHwv+r72
JqI6tdyl7LARK4sq/mgvZNgORkuxlHld4lz/lANrHvNq+FuZb6O/pMknsYyIwPFa
9EdON5JbEgYx7zdUpAzm5x7B7b3wS1UGRWQa4zGSCXX9KRS8Ckm2Namhpqqwnsj6
P2lGoTwBzxi+EJbhsQIDAQABozIwMDAdBgNVHQ4EFgQUz+RHV+DzDpytNATIpVx0
Xl2mUZswDwYDVR0TAQH/BAUwAwEB/zANBgkqhkiG9w0BAQsFAAOCAQEALhR3z3DH
t+7RO4GIVgXqdCd9g1IAQ/FBMHInJBW6yaPSjLM60u41x6IvYf5v6aWcJI/PoSES
/JgtMcEkVl/Cc5O4eG7SVdqulwIF7CwUiQe2fMxyF6sTZEj9ONBNxaa/D5YUfjoh
d5PKdkmB+bvfoTKo6b5RigdW5DuwswB9jJ7wNYYGvW6RBru8QdKxJwAUUIOj3g7k
fVHgWbVJvf2MPq5vlJ7BffPxIQLm4q8IE3nqAxWt+64umhDSOfVnIb5oAzmNw/KF
M0+ZkLhC3MW1bKT0fj1rwkseIlhgrIasb0hCQL/SXVa21mAzZQx7/G86Ei+0tat+
/oMvpbiFpKzRig==
-----END CERTIFICATE-----`

const later = `-----BEGIN CERTIFICATE-----
MIIDBDCCAeygAwIBAgIUVWiLmKi/xgRXioDluzD+PNCSWLkwDQYJKoZIhvcNAQEL
BQAwIjEgMB4GA1UEAwwXa3l2ZXJuby1zdmMua3l2ZXJuby5zdmMwHhcNMjYxMTA4
MTI1NTQ2WhcNMzYxMTA1MTI1NTQ2WjAiMSAwHgYDVQQDDBdreXZlcm5vLXN2Yy5r
eXZlcm5vLnN2YzCCASIwDQYJKoZIhvcNAQEBBQADggEPADCCAQoCggEBANE9jYsJ
6Za6EnXDqeklwpG+bMlzB5OO0qtDQ2bqEy2flVcLTZwyIkfaFE10T9pD3vZwNruf
i2roOpiOnyhBl+qCqWYNPLd+jxTb4dKvQz1Gqvf53cVZinBBBU4fdbbOI34poPTv
tszU4wmZVN0O7EZlIDBlgUpSMXdjOrmew4pwZQRzaqkbz26tEPEB2VL7K31MOPct
zdp25t7TsKX1fItjQEsJ8GzJD1bj8CiS+8Z06I0+SPNBO9Gn4Wtyll8JSFHW/nuo
g+ke00x/VZaUi5kMcLcd+5KT+dXHnvt6WQFshsEUa/Te1dtzIvXc2FpHNSA+YySA
4xV5/viMuvIGUhUCAwEAAaMyMDAwHQYDVR0OBBYEFCm+k8cd4Lo11Rp0fi7XQ/Vu
r3ZhMA8GA1UdEwEB/wQFMAMBAf8wDQYJKoZIhvcNAQELBQADggEBALOWRw7i6uEn
SLuH9hB1YUqWA92g6vOfCCg7aANgW2GyNuoGJTXComAglaR3NO5wRZrUL8c7Drfh
mAjPwZa01YiK4mV52ZWSRxjXEtVsIkl5kzt7xUSWrVh7MoZ1ZtxMcsGo4azwp6gn
Cryrl+mqd4qtzFxXmQ79v2uKB1ao3RnYaL03nggxR9vB3DZrTSqLNrdS9R1J0UCy
IqX457fkjzcPrXlBEbsG5L9BGescsKFQgbg3ikODx8YtK0A6WGckkrj9ZzlwJFI2
dzCIAxlnoTQi1LE0UUYwDwjl3xWR4l8Jq09Fp95Qzlur1972aNUwfdgaaVQyAzuh
UmKWeOBdeHs=
-----END CERTIFICATE-----`

// TestParseBundleLatestWins: a bundle is a trust pool; during CA rotation
// the old CA expiring soon does not break the webhook.
func TestParseBundleLatestWins(t *testing.T) {
	c, ok := query.ParseBundle([]byte(soon + "\n" + later))
	if !ok || c.NotAfter.Year() != 2036 || c.CommonName != "kyverno-svc.kyverno.svc" {
		t.Errorf("ParseBundle = %+v %v; want the 2036 cert", c, ok)
	}
	if c, _ := query.ParseBundle([]byte(soon)); c.NotAfter.Year() != 2026 {
		t.Errorf("single cert = %v", c.NotAfter)
	}
	if _, ok := query.ParseBundle([]byte("garbage")); ok {
		t.Error("garbage must not parse")
	}
}

func TestCheckWebhooks(t *testing.T) {
	ready := []query.Endpoint{{Pod: "ns/a", Ready: true}}
	down := []query.Endpoint{{Pod: "ns/a", Ready: false}}
	tests := []struct {
		name   string
		w      query.Webhook
		status query.Status
		reason string
	}{
		{"fail-open without endpoints", query.Webhook{FailurePolicy: "Ignore", Service: "ns/s", EndpointsKnown: true, Endpoints: down}, query.Critical, "fail-open, no endpoints"},
		{"fail-closed without endpoints", query.Webhook{FailurePolicy: "Fail", Service: "ns/s", EndpointsKnown: true}, query.Warning, "no endpoints: admission blocked"},
		{"unreadable endpoints are never 'none'", query.Webhook{FailurePolicy: "Ignore", Service: "ns/s"}, query.Unknown, "endpoints unknown"},
		{"cert expiring", query.Webhook{FailurePolicy: "Fail", Service: "ns/s", EndpointsKnown: true, Endpoints: ready, CABundle: []byte(soon)}, query.Warning, "cert expires in 7d"},
		{"healthy", query.Webhook{FailurePolicy: "Ignore", Service: "ns/s", EndpointsKnown: true, Endpoints: ready, CABundle: []byte(later)}, query.OK, ""},
		{"URL webhook has no endpoints to count", query.Webhook{FailurePolicy: "Ignore", URL: "https://x"}, query.OK, ""},
	}
	for _, tt := range tests {
		rows := query.CheckWebhooks(query.Platform{Webhooks: []query.Webhook{tt.w}}, fixtures.Now)
		if rows[0].Status != tt.status || rows[0].Reason != tt.reason {
			t.Errorf("%s: %s %q, want %s %q", tt.name, rows[0].Status, rows[0].Reason, tt.status, tt.reason)
		}
	}
	expired := query.CheckWebhooks(query.Platform{Webhooks: []query.Webhook{
		{FailurePolicy: "Fail", Service: "ns/s", EndpointsKnown: true, Endpoints: ready, CABundle: []byte(soon)},
	}}, fixtures.Now.Add(8*24*time.Hour))
	if expired[0].Status != query.Critical || expired[0].Reason != "cert expired" {
		t.Errorf("expired bundle = %s %q, want critical", expired[0].Status, expired[0].Reason)
	}
	rows := query.CheckWebhooks(query.Platform{Webhooks: []query.Webhook{
		{Name: "b", FailurePolicy: "Fail", Service: "ns/s", EndpointsKnown: true, Endpoints: ready},
		{Name: "a", FailurePolicy: "Ignore", Service: "ns/s", EndpointsKnown: true},
	}}, fixtures.Now)
	if rows[0].Name != "a" {
		t.Error("worst first")
	}
}

func TestCheckControllers(t *testing.T) {
	now := fixtures.Now
	p := query.Platform{Controllers: []query.Controller{
		{Engine: "e", Name: "down", Ready: 0, Desired: 2, Images: []string{"img:v1"}},
		{Engine: "e", Name: "flappy", Ready: 1, Desired: 1, LastRestart: now.Add(-10 * time.Minute), Images: []string{"img:v1"}},
		{Engine: "e", Name: "old-restart", Ready: 1, Desired: 1, LastRestart: now.Add(-3 * time.Hour), Images: []string{"img:v1"}},
		{Engine: "f", Name: "a", Ready: 1, Desired: 1, Images: []string{"reg/f:v2@sha256:abc"}},
		{Engine: "f", Name: "b", Ready: 1, Desired: 1, Images: []string{"reg/f-bg:v3"}},
		// g: two on v1, one on v2: only the odd one out warns.
		{Engine: "g", Name: "g1", Ready: 1, Desired: 1, Images: []string{"g:v1"}},
		{Engine: "g", Name: "g2", Ready: 1, Desired: 1, Images: []string{"g-bg:v1"}},
		{Engine: "g", Name: "g3", Ready: 1, Desired: 1, Images: []string{"g-rep:v2"}},
	}}
	got := map[string]query.ControllerRow{}
	for _, r := range query.CheckControllers(p, now) {
		got[r.Name] = r
	}
	// f: one each (a tie, no majority) so both warn.
	want := map[string]query.Status{"down": query.Critical, "flappy": query.Warning, "old-restart": query.OK, "a": query.Warning, "b": query.Warning,
		"g1": query.OK, "g2": query.OK, "g3": query.Warning}
	for name, s := range want {
		if got[name].Status != s {
			t.Errorf("%s: %s (%s), want %s", name, got[name].Status, got[name].Reason, s)
		}
	}
	if got["a"].Tag != "v2" {
		t.Errorf("digest refs keep their tag: %q", got["a"].Tag)
	}
}

func TestOverallCounts(t *testing.T) {
	var o query.Overall
	for _, s := range []query.Status{query.Critical, query.Warning, query.Warning, query.Unknown, query.OK} {
		o.Add(s)
	}
	if o != (query.Overall{Critical: 1, Warning: 2, Unknown: 1, OK: 1}) {
		t.Errorf("overall = %+v", o)
	}
	if query.ShortDuration(49*time.Hour) != "2d" || query.ShortDuration(90*time.Second) != "1m" {
		t.Error("ShortDuration")
	}
}

type platformEngine struct{ *fake.Engine }

func (platformEngine) PlatformInfo() engine.PlatformInfo {
	return engine.PlatformInfo{Namespace: "ns", AuditSource: "reports"}
}
func (platformEngine) PolicyStatus() engine.PolicyStatus {
	return engine.PolicyStatus{Total: 2, Loaded: 2}
}

func TestUnsyncedEnginesAreUnknown(t *testing.T) {
	e := fake.New(fake.Options{Name: "e", Installed: true, Unsynced: true, Caps: fixtures.AllCaps})
	if err := e.Start(t.Context(), nil, func() {}); err != nil {
		t.Fatal(err)
	}
	es := []engine.Engine{platformEngine{e}}
	if a := query.CheckAudit(es, query.Platform{}, fixtures.Now); a[0].Status != query.Unknown {
		t.Errorf("audit of an unsynced engine = %s, want unknown", a[0].Status)
	}
	if p := query.CheckPolicies(es); p[0].Status != query.Unknown {
		t.Errorf("policies of an unsynced engine = %s, want unknown", p[0].Status)
	}
	e.SetSynced(true)
	if p := query.CheckPolicies(es); p[0].Status != query.OK {
		t.Errorf("synced = %s %q", p[0].Status, p[0].Reason)
	}
}

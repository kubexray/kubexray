package prom

import (
	"context"
	"strings"
	"testing"
)

// fake answers queries by a substring of them.
type fake map[string][]Sample

func (f fake) Query(_ context.Context, q string) ([]Sample, error) {
	for k, v := range f {
		if strings.Contains(q, k) {
			return v, nil
		}
	}
	return nil, nil
}

func (fake) Where() string { return "fake" }

func TestTraefikTarget(t *testing.T) {
	t.Parallel()
	known := func(ns, name string) bool {
		return (ns == "shop" && name == "api") || (ns == "shop-eu" && name == "check-out")
	}
	for in, want := range map[string]string{
		"shop-api-80@kubernetes":               "shop/api",
		"shop-eu-check-out-8080@kubernetescrd": "shop-eu/check-out",
		"shop-nope-80@kubernetes":              "",
		"api@file":                             "",
	} {
		ns, name, ok := traefikTarget(map[string]string{"service": in}, known)
		if got := ns + "/" + name; (ok && got != want) || (!ok && want != "") {
			t.Errorf("%s = %s, %v; want %q", in, got, ok, want)
		}
	}
}

func TestCollect(t *testing.T) {
	t.Parallel()
	svc := func(s string) map[string]string { return map[string]string{"service": s} }
	q := fake{
		`code=~"5.."`:                        {{Labels: svc("shop-api-80@kubernetes"), Value: 2}},
		"histogram_quantile":                 {{Labels: svc("shop-api-80@kubernetes"), Value: 1.5}},
		"traefik_service_requests_total[2m]": {{Labels: svc("shop-api-80@kubernetes"), Value: 8}, {Labels: svc("shop-gone-80@kubernetes"), Value: 1}},
	}
	known := func(ns, name string) bool { return ns == "shop" && name == "api" }
	got, err := Collect(context.Background(), q, Adapters[0], known)
	if err != nil {
		t.Fatal(err)
	}
	if n := got[Key{"shop", "api"}]; n.RPS != 8 || n.Errors != 2 || n.P99 != 1.5 || len(got) != 1 {
		t.Errorf("got %+v", got)
	}
}

func TestDetect(t *testing.T) {
	t.Parallel()
	q := fake{"group by": {{Labels: map[string]string{"__name__": "istio_requests_total"}}, {Labels: map[string]string{"__name__": "traefik_service_requests_total"}}}}
	got, err := Detect(context.Background(), q)
	if err != nil || len(got) != 2 || got[0].Name != "Traefik" || got[1].Name != "Istio" {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	s, err := parse([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"service":"a"},"value":[1,"0.5"]},{"metric":{},"value":[1,"NaN"]}]}}`))
	if err != nil || len(s) != 2 || s[0].Value != 0.5 {
		t.Errorf("got %+v, %v", s, err)
	}
	if _, err := parse([]byte(`{"status":"error","error":"bad query"}`)); err == nil || !strings.Contains(err.Error(), "bad query") {
		t.Errorf("err = %v", err)
	}
}

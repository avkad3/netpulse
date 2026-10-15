package weather

import (
	"testing"

	"github.com/avkad3/netpulse/internal/store"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		s    store.Stats
		want Condition
	}{
		{"no data", store.Stats{}, Unknown},
		{"healthy", store.Stats{Count: 100, UptimePct: 100, P95Ms: 80}, Sunny},
		{"slow", store.Stats{Count: 100, UptimePct: 100, P95Ms: 400}, Cloudy},
		{"few failures", store.Stats{Count: 100, UptimePct: 98, P95Ms: 80}, Cloudy},
		{"very slow", store.Stats{Count: 100, UptimePct: 100, P95Ms: 2000}, Rainy},
		{"flaky", store.Stats{Count: 100, UptimePct: 90, P95Ms: 80}, Rainy},
		{"down", store.Stats{Count: 100, UptimePct: 20}, Stormy},
	}
	for _, c := range cases {
		if got := Default.Classify(c.s).Condition; got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestOverall(t *testing.T) {
	r := func(cs ...Condition) []Report {
		out := make([]Report, len(cs))
		for i, c := range cs {
			out[i] = Report{Condition: c}
		}
		return out
	}
	cases := []struct {
		in   []Report
		want Condition
	}{
		{r(), Unknown},
		{r(Unknown, Unknown), Unknown},
		{r(Sunny, Sunny, Sunny), Sunny},
		{r(Sunny, Sunny, Sunny, Cloudy), Cloudy},
		{r(Sunny, Sunny, Sunny, Stormy), Rainy},
		{r(Stormy, Stormy, Sunny, Sunny), Stormy},
		{r(Cloudy, Cloudy, Sunny), Cloudy},
	}
	for i, c := range cases {
		if got := Overall(c.in); got != c.want {
			t.Errorf("case %d: got %s, want %s", i, got, c.want)
		}
	}
	if !Worse(Stormy, Sunny) || Worse(Sunny, Cloudy) {
		t.Error("Worse ordering wrong")
	}
}

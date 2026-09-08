package relay

// The relay turns a registry row into an answer in one place, and two rules
// decide what a caller is told: which EndpointAdmission a row projects to, and
// which of several matching rows wins. Both are plain classification with no
// registry behind them, so they are pinned here rather than through a route.

import (
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
)

// TestAdmissionOfProjectsEveryRegistryVerdict pins the projection from the
// registry's rule onto what a relay caller is told.
//
// The refused row is the one that has to keep its own value. It used to arrive
// as EndpointDown, which is the retryable answer, so an Exchange publishing a
// manifest this build cannot read was reported as a momentary outage and an
// agent retried it forever. BLOCKED collapsing onto UNREGISTERED is deliberate
// and stays.
func TestAdmissionOfProjectsEveryRegistryVerdict(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		ex   repo.Exchange
		want EndpointAdmission
	}{
		"answering": {
			repo.Exchange{TrustLevel: repo.TrustLevelVerified, ProbeState: repo.ProbeLive},
			EndpointLive,
		},
		"unreachable": {
			repo.Exchange{TrustLevel: repo.TrustLevelVerified, ProbeState: repo.ProbeDown},
			EndpointDown,
		},
		"manifest refused": {
			repo.Exchange{TrustLevel: repo.TrustLevelVerified, ProbeState: repo.ProbeRefused},
			EndpointRefused,
		},
		"trust withdrawn": {
			repo.Exchange{TrustLevel: repo.TrustLevelBlocked, ProbeState: repo.ProbeLive},
			EndpointUnregistered,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := admissionOf(tc.ex); got != tc.want {
				t.Errorf("admissionOf = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBestAdmissionForPicksTheMostPermissiveMatch pins the tie-break the
// EndpointAdmission constants encode in their order.
//
// Nothing makes the endpoint column unique, so two registered domains whose
// well-knowns advertise one origin both carry it, and the refresher actively
// brings that about. One of them being unusable says nothing about the other,
// which is why the most permissive matching row wins.
//
// The two mixed rows are what the ordering is for. A refused Exchange beside an
// unreachable one must answer with the unreachable one: the caller may retry
// and find it back, and a settled refusal about a DIFFERENT Exchange must not
// take that away. A refused Exchange beside no match at all must answer refused,
// because a row does carry the address and "not a registered exchange" would be
// false about it.
func TestBestAdmissionForPicksTheMostPermissiveMatch(t *testing.T) {
	t.Parallel()

	const target = "https://exchange.example"
	row := func(domain string, state repo.ProbeState, endpoint string) repo.Exchange {
		return repo.Exchange{
			Domain:     domain,
			Endpoint:   endpoint,
			TrustLevel: repo.TrustLevelVerified,
			ProbeState: state,
		}
	}

	for name, tc := range map[string]struct {
		rows []repo.Exchange
		want EndpointAdmission
	}{
		"nothing advertises it": {
			[]repo.Exchange{row("a.example", repo.ProbeLive, "https://other.example")},
			EndpointUnregistered,
		},
		"one refused row": {
			[]repo.Exchange{row("a.example", repo.ProbeRefused, target)},
			EndpointRefused,
		},
		"refused beside unreachable": {
			[]repo.Exchange{
				row("a.example", repo.ProbeRefused, target),
				row("b.example", repo.ProbeDown, target),
			},
			EndpointDown,
		},
		"refused beside answering": {
			[]repo.Exchange{
				row("a.example", repo.ProbeRefused, target),
				row("b.example", repo.ProbeLive, target),
			},
			EndpointLive,
		},
		"refused beside a row on another address": {
			[]repo.Exchange{
				row("a.example", repo.ProbeRefused, target),
				row("b.example", repo.ProbeLive, "https://other.example"),
			},
			EndpointRefused,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := bestAdmissionFor(tc.rows, target); got != tc.want {
				t.Errorf("bestAdmissionFor = %v, want %v", got, tc.want)
			}
		})
	}
}

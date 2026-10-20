package sitemapwalk_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sitemapwalk"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sitemapwalk/sitemaptest"
)

// No t.Parallel anywhere in this file: the guarded transport reads the two
// deployment opt-outs from the environment -- SKIP_SSRF when it is built,
// ALLOW_INSECURE on every request -- so every test that dials a loopback peer
// sets them via t.Setenv. A guard test sets them so that only the guard it
// names can refuse, and opens the other one on purpose: a test that two guards
// could both pass keeps passing when the one it names is gone.

// pages renders n page URLs on a fictional site.
func pages(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("https://publisher.example/news/story-%d", i))
	}
	return out
}

// openClient builds the production client with the deployment opt-outs set,
// so it can reach the loopback origins these tests serve.
func openClient(t *testing.T) *http.Client {
	t.Helper()
	testutil.AllowLoopbackFetch(t)
	return sitemapwalk.NewClient(5 * time.Second)
}

func walk(t *testing.T, client sitemapwalk.HTTPDoer, root string, limits sitemapwalk.Limits) sitemapwalk.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return sitemapwalk.Walk(ctx, client, root, limits)
}

func wantComplete(t *testing.T, r sitemapwalk.Result, urls int) {
	t.Helper()
	if !r.Complete || r.Reason != "" || r.Err != nil {
		t.Fatalf("walk incomplete: reason=%q err=%v", r.Reason, r.Err)
	}
	if len(r.URLs) != urls {
		t.Fatalf("walk yielded %d urls, want %d: %v", len(r.URLs), urls, r.URLs)
	}
}

func wantStopped(t *testing.T, r sitemapwalk.Result, reason sitemapwalk.Reason) {
	t.Helper()
	if r.Complete || r.Reason != reason || r.Err == nil {
		t.Fatalf("walk = complete=%v reason=%q err=%v; want incomplete with reason %q and an error",
			r.Complete, r.Reason, r.Err, reason)
	}
}

func TestWalk_SingleURLSet(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	root := o.Doc("/sitemap.xml", sitemaptest.Urlset(pages(2)...))
	r := walk(t, openClient(t), root, sitemapwalk.Limits{})
	wantComplete(t, r, 2)
	if r.Documents != 1 || r.URLs[0] != pages(2)[0] {
		t.Errorf("result = %+v", r)
	}
}

func TestWalk_IndexWithChildren(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	p := pages(5)
	root := o.Doc("/sitemap.xml", sitemaptest.Index(
		o.Doc("/a.xml", sitemaptest.Urlset(p[0], p[1])),
		o.Doc("/b.xml", sitemaptest.Urlset(p[2])),
		o.Doc("/c.xml", sitemaptest.Urlset(p[3], p[4])),
	))
	r := walk(t, openClient(t), root, sitemapwalk.Limits{})
	wantComplete(t, r, 5)
	if r.Documents != 4 {
		t.Errorf("fetched %d documents, want 4 (root + 3 children)", r.Documents)
	}
	for _, path := range []string{"/sitemap.xml", "/a.xml", "/b.xml", "/c.xml"} {
		if o.Hits(path) != 1 {
			t.Errorf("%s fetched %d times, want once", path, o.Hits(path))
		}
	}
}

// An index naming itself, and a child naming the index back, each end the walk
// instead of looping it: a sitemap is fetched once.
func TestWalk_CyclesTerminate(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	root := o.URL + "/sitemap.xml"
	child := o.Doc("/child.xml", sitemaptest.Index(root, o.Doc("/leaf.xml", sitemaptest.Urlset(pages(1)...))))
	o.Doc("/sitemap.xml", sitemaptest.Index(root, child))
	r := walk(t, openClient(t), root, sitemapwalk.Limits{})
	wantComplete(t, r, 1)
	if r.Documents != 3 || o.Hits("/sitemap.xml") != 1 || o.Hits("/child.xml") != 1 {
		t.Errorf("cycle was re-fetched: documents=%d, /sitemap.xml hits=%d, /child.xml hits=%d",
			r.Documents, o.Hits("/sitemap.xml"), o.Hits("/child.xml"))
	}
}

// A page listed twice -- within one document or across two -- takes one slot,
// including when the duplicates arrive with one slot left.
func TestWalk_DuplicatesCollapseAndDoNotSpendTheBudget(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	p := pages(3)
	root := o.Doc("/sitemap.xml", sitemaptest.Index(
		o.Doc("/a.xml", sitemaptest.Urlset(p[0], p[1], p[0])),
		o.Doc("/b.xml", sitemaptest.Urlset(p[0], p[0], p[2])),
	))
	r := walk(t, openClient(t), root, sitemapwalk.Limits{MaxResources: 3})
	wantComplete(t, r, 3)
	if r.URLs[0] != p[0] || r.URLs[1] != p[1] || r.URLs[2] != p[2] {
		t.Errorf("urls = %v, want first-seen order without duplicates", r.URLs)
	}
}

func TestWalk_ResourceCapExactIsCompleteOnePastIsNot(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	exact := o.Doc("/exact.xml", sitemaptest.Urlset(pages(4)...))
	over := o.Doc("/over.xml", sitemaptest.Urlset(pages(5)...))
	client := openClient(t)
	wantComplete(t, walk(t, client, exact, sitemapwalk.Limits{MaxResources: 4}), 4)
	r := walk(t, client, over, sitemapwalk.Limits{MaxResources: 4})
	wantStopped(t, r, sitemapwalk.ReasonResources)
}

// The document cap counts sitemaps fetched, separately from the pages they
// list: three children under a cap of two stop the walk with the document
// reason while the resource budget is untouched.
func TestWalk_DocumentCapIsSeparateFromResources(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	root := o.Doc("/sitemap.xml", sitemaptest.Index(
		o.Doc("/a.xml", sitemaptest.Urlset()),
		o.Doc("/b.xml", sitemaptest.Urlset()),
		o.Doc("/c.xml", sitemaptest.Urlset()),
	))
	r := walk(t, openClient(t), root, sitemapwalk.Limits{MaxDocuments: 2, MaxResources: 1})
	wantStopped(t, r, sitemapwalk.ReasonDocuments)
	if r.Documents != 2 || o.Hits("/c.xml") != 0 {
		t.Errorf("documents=%d, /c.xml hits=%d; want 2 fetched and the third never dialled", r.Documents, o.Hits("/c.xml"))
	}
}

// Malformed XML after the last page is still a parser failure: the document is
// read whole, not only as far as the pages it lists.
func TestWalk_MalformedTailIsAParserFailure(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	body := strings.TrimSuffix(sitemaptest.Urlset(pages(2)...), "</urlset>") + "<url><loc>https://publisher.example/x</loc></url"
	root := o.Doc("/sitemap.xml", body)
	r := walk(t, openClient(t), root, sitemapwalk.Limits{MaxResources: 2})
	wantStopped(t, r, sitemapwalk.ReasonParser)
}

func TestWalk_NotASitemapIsAParserFailure(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	root := o.Doc("/sitemap.xml", "<html><body>home page</body></html>")
	wantStopped(t, walk(t, openClient(t), root, sitemapwalk.Limits{}), sitemapwalk.ReasonParser)
}

func TestWalk_SizeCap(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	root := o.Doc("/sitemap.xml", sitemaptest.Urlset(pages(50)...))
	r := walk(t, openClient(t), root, sitemapwalk.Limits{MaxDocBytes: 512})
	wantStopped(t, r, sitemapwalk.ReasonSize)
	if !errors.Is(r.Err, sitemapwalk.ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", r.Err)
	}
}

// stall holds a handler until the client gives up on the request or d passes.
func stall(r *http.Request, d time.Duration) {
	select {
	case <-r.Context().Done():
	case <-time.After(d):
	}
}

func TestWalk_Deadline(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	root := o.Handle("/slow.xml", func(w http.ResponseWriter, r *http.Request) {
		stall(r, 3*time.Second)
		_, _ = w.Write([]byte(sitemaptest.Urlset()))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r := sitemapwalk.Walk(ctx, openClient(t), root, sitemapwalk.Limits{})
	wantStopped(t, r, sitemapwalk.ReasonDeadline)
}

// A site that does not answer within the client's per-request timeout stops
// the walk as request-timeout while the caller's deadline is still far off, so
// the reason names the setting that ran out rather than the whole walk's.
func TestWalk_RequestTimeoutIsNotTheDeadline(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	root := o.Handle("/slow.xml", func(w http.ResponseWriter, r *http.Request) {
		stall(r, 3*time.Second)
		_, _ = w.Write([]byte(sitemaptest.Urlset()))
	})
	testutil.AllowLoopbackFetch(t)
	r := walk(t, sitemapwalk.NewClient(200*time.Millisecond), root, sitemapwalk.Limits{})
	wantStopped(t, r, sitemapwalk.ReasonRequestTimeout)
}

// The per-request timeout covers reading the document too: a site that
// answers and then stalls mid-document stops the walk the same way.
func TestWalk_RequestTimeoutWhileReadingTheBody(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	root := o.Handle("/stalls.xml", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><urlset>`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		stall(r, 3*time.Second)
	})
	testutil.AllowLoopbackFetch(t)
	r := walk(t, sitemapwalk.NewClient(200*time.Millisecond), root, sitemapwalk.Limits{})
	wantStopped(t, r, sitemapwalk.ReasonRequestTimeout)
	if !strings.Contains(r.Err.Error(), "sitemapwalk: read ") {
		t.Errorf("the walk stopped before the document was read: %v", r.Err)
	}
}

func TestWalk_EmptyURLSetIsComplete(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	root := o.Doc("/sitemap.xml", sitemaptest.Urlset())
	wantComplete(t, walk(t, openClient(t), root, sitemapwalk.Limits{}), 0)
}

func TestWalk_NotFoundIsASourceFailure(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	wantStopped(t, walk(t, openClient(t), o.URL+"/missing.xml", sitemapwalk.Limits{}), sitemapwalk.ReasonSource)
}

// addressGuardedClient builds the production client with the dial-time
// ADDRESS guard armed and the scheme guard opened on purpose: SKIP_SSRF is
// cleared, and ALLOW_INSECURE lets plain http through, so the only thing that
// can refuse the plain-http loopback origin these tests serve is the address
// guard. Both are set rather than assumed absent, because every other test in
// this file opens them and a value inherited from the environment would decide
// the result.
func addressGuardedClient(t *testing.T) *http.Client {
	t.Helper()
	t.Setenv("SKIP_SSRF", "")
	t.Setenv("ALLOW_INSECURE", "1")
	return sitemapwalk.NewClient(5 * time.Second)
}

// schemeOnlyClient builds the production client with the address guard
// switched off and the scheme guard left at its default, https only, so the
// only thing that can refuse a plain-http origin is the scheme guard.
func schemeOnlyClient(t *testing.T) *http.Client {
	t.Helper()
	t.Setenv("SKIP_SSRF", "1")
	t.Setenv("ALLOW_INSECURE", "")
	return sitemapwalk.NewClient(5 * time.Second)
}

// A root on a prohibited destination is refused by the address guard before a
// connection is made: the origin sees no request.
func TestWalk_ProhibitedRootIsRefused(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	root := o.Doc("/sitemap.xml", sitemaptest.Urlset(pages(1)...))
	r := walk(t, addressGuardedClient(t), root, sitemapwalk.Limits{})
	wantStopped(t, r, sitemapwalk.ReasonSource)
	if o.Hits("/sitemap.xml") != 0 {
		t.Errorf("the guarded client dialled a loopback root %d time(s)", o.Hits("/sitemap.xml"))
	}
}

// inlineRoot answers the root from memory and hands every other address to the
// real client, so a child sitemap can be shown to go through the guard even
// when no allowed origin can serve the root in a test.
type inlineRoot struct {
	root string
	body string
	next sitemapwalk.HTTPDoer
}

func (d inlineRoot) Do(req *http.Request) (*http.Response, error) {
	if req.URL.String() != d.root {
		return d.next.Do(req)
	}
	rec := httptest.NewRecorder()
	_, _ = rec.WriteString(d.body)
	return rec.Result(), nil
}

// A child sitemap on a prohibited destination is refused by the same guarded
// client the root went through.
func TestWalk_ProhibitedChildIsRefused(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	child := o.Doc("/child.xml", sitemaptest.Urlset(pages(1)...))
	const root = "https://publisher.example/sitemap.xml"
	client := inlineRoot{root: root, body: sitemaptest.Index(child), next: addressGuardedClient(t)}
	r := walk(t, client, root, sitemapwalk.Limits{})
	wantStopped(t, r, sitemapwalk.ReasonSource)
	if o.Hits("/child.xml") != 0 {
		t.Errorf("the guarded client dialled a loopback child %d time(s)", o.Hits("/child.xml"))
	}
}

// A sitemap served over plain http is refused in the default posture, with no
// request sent: the production client reads https only unless the deployment
// sets ALLOW_INSECURE.
func TestWalk_PlainHTTPIsRefusedByDefault(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	root := o.Doc("/sitemap.xml", sitemaptest.Urlset(pages(1)...))
	r := walk(t, schemeOnlyClient(t), root, sitemapwalk.Limits{})
	wantStopped(t, r, sitemapwalk.ReasonSource)
	if o.Hits("/sitemap.xml") != 0 {
		t.Errorf("the guarded client sent %d request(s) to a plain-http root", o.Hits("/sitemap.xml"))
	}
}

func TestWalk_CredentialsInAddressAreRefused(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	o.Doc("/sitemap.xml", sitemaptest.Urlset())
	withUser := strings.Replace(o.URL, "http://", "http://user:secret@", 1) + "/sitemap.xml"
	r := walk(t, openClient(t), withUser, sitemapwalk.Limits{})
	wantStopped(t, r, sitemapwalk.ReasonSource)
	if !errors.Is(r.Err, sitemapwalk.ErrCredentialsInURL) {
		t.Errorf("err = %v, want ErrCredentialsInURL", r.Err)
	}
	if strings.Contains(r.Err.Error(), "secret") {
		t.Errorf("the error repeats the credential: %v", r.Err)
	}
	if o.Hits("/sitemap.xml") != 0 {
		t.Error("an address carrying credentials was dialled")
	}
}

// withUserinfo is the origin's address with userinfo written into it.
func withUserinfo(o *sitemaptest.Origin, userinfo string) string {
	return strings.Replace(o.URL, "http://", "http://"+userinfo+"@", 1)
}

// A child sitemap whose address carries a secret never has the secret repeated
// in the walk's error, whichever check stops the walk on it, and the error
// names the sitemap that listed the child, which is what finds a child whose
// own address is too malformed to show.
func TestWalk_ErrorsNeverRepeatCredentials(t *testing.T) {
	for _, tc := range []struct {
		name   string
		child  func(o *sitemaptest.Origin) string
		limits sitemapwalk.Limits
		reason sitemapwalk.Reason
		secret string
	}{
		{
			name:   "past the document limit",
			child:  func(o *sitemaptest.Origin) string { return withUserinfo(o, "user:secret") + "/second.xml" },
			limits: sitemapwalk.Limits{MaxDocuments: 2}, reason: sitemapwalk.ReasonDocuments, secret: "secret",
		},
		{
			name:   "an address that does not parse",
			child:  func(o *sitemaptest.Origin) string { return withUserinfo(o, "user:secret") + "/%zz.xml" },
			reason: sitemapwalk.ReasonSource, secret: "secret",
		},
		{
			name: "a scheme and no host",
			child: func(o *sitemaptest.Origin) string {
				return "https:user:secret@" + strings.TrimPrefix(o.URL, "http://") + "/x.xml"
			},
			reason: sitemapwalk.ReasonSource, secret: "secret",
		},
		{
			name:   "a token in the user-name slot",
			child:  func(o *sitemaptest.Origin) string { return withUserinfo(o, "tokenvalue") + "/c.xml" },
			reason: sitemapwalk.ReasonSource, secret: "tokenvalue",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := sitemaptest.NewOrigin(t)
			first := o.Doc("/first.xml", sitemaptest.Urlset())
			root := o.Doc("/sitemap.xml", sitemaptest.Index(first, tc.child(o)))
			r := walk(t, openClient(t), root, tc.limits)
			wantStopped(t, r, tc.reason)
			if msg := r.Err.Error(); strings.Contains(msg, tc.secret) || !strings.Contains(msg, "listed in "+root) {
				t.Errorf("the error repeats the secret or does not name the index that listed the child: %s", msg)
			}
		})
	}
}

// A refused redirect names where the origin tried to send the walk without the
// credentials that address carries, and the target is never fetched.
func TestWalk_RefusedRedirectNeverRepeatsCredentials(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	o.Doc("/moved.xml", sitemaptest.Urlset(pages(1)...))
	target := withUserinfo(o, "user:secret") + "/moved.xml"
	root := o.Handle("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	})
	r := walk(t, openClient(t), root, sitemapwalk.Limits{})
	wantStopped(t, r, sitemapwalk.ReasonSource)
	if !errors.Is(r.Err, sitemapwalk.ErrRedirectRefused) || strings.Contains(r.Err.Error(), "secret") {
		t.Errorf("err = %v; want a refused redirect that does not repeat the secret", r.Err)
	}
	if o.Hits("/moved.xml") != 0 {
		t.Error("the redirect target was fetched")
	}
}

func TestWalk_RedirectIsRefused(t *testing.T) {
	o := sitemaptest.NewOrigin(t)
	target := o.Doc("/moved.xml", sitemaptest.Urlset(pages(1)...))
	root := o.Handle("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	})
	r := walk(t, openClient(t), root, sitemapwalk.Limits{})
	wantStopped(t, r, sitemapwalk.ReasonSource)
	if !errors.Is(r.Err, sitemapwalk.ErrRedirectRefused) {
		t.Errorf("err = %v, want ErrRedirectRefused", r.Err)
	}
	if o.Hits("/moved.xml") != 0 {
		t.Error("the redirect target was fetched")
	}
}

package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/ollama"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
	"github.com/labbersanon/sakms/internal/usenetsearch"
	"github.com/labbersanon/sakms/internal/websearch"
)

const maxDiscoveredPasswords = 5

// Claude 2026-10-11: Discover/native/AI password lookup for encrypted RARs.
// Reason: fallback after this NZB's file subjects and nfo/txt BODY peeks miss.
//   Indexer titles may still carry {{pw}} / password=; AI is opt-in.
// Troubleshooting: journal "usenet password discover:" then engine
//   "discovered N unpack password candidate(s)". Never logs the value.
// Review if: a per-job UI password field replaces search.
// Related: usenet.peekPostPasswords; SetPasswordDiscoverer.

func attachUsenetPasswordDiscoverer(nzb *usenet.Manager, httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, dl *downloader.Manager) {
	if nzb == nil || connStore == nil || settingsStore == nil {
		return
	}
	nzb.SetPasswordDiscoverer(func(ctx context.Context, title string) []string {
		return discoverUsenetPasswords(ctx, httpClient, connStore, scStore, settingsStore, dl, title)
	})
}

func discoverUsenetPasswords(ctx context.Context, httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, dl *downloader.Manager, title string) []string {
	query := usenet.DiscoverySearchQuery(title)
	if query == "" {
		return nil
	}
	var found []string
	sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, dl, mode.Movies)
	if err != nil {
		log.Printf("usenet password discover: session: %v", err)
	}
	if sess != nil {
		found = append(found, passwordsFromProwlarr(ctx, sess.Prowlarr, query)...)
	}
	found = append(found, passwordsFromNativeSearch(ctx, query)...)
	if len(found) == 0 && sess != nil {
		found = append(found, passwordFromWebAI(ctx, sess.WebSearch, sess.MainstreamAI, query)...)
	}
	return uniquePasswords(found, maxDiscoveredPasswords)
}

func uniquePasswords(in []string, max int) []string {
	seen := map[string]bool{}
	var out []string
	for _, pw := range in {
		pw = strings.TrimSpace(pw)
		if pw == "" || seen[pw] {
			continue
		}
		seen[pw] = true
		out = append(out, pw)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

func passwordsFromProwlarr(ctx context.Context, client *prowlarr.Client, query string) []string {
	if client == nil {
		return nil
	}
	releases, err := client.SearchByID(ctx, prowlarr.SearchByIDParams{
		Query: query,
		Scope: prowlarr.ScopeUsenet,
	})
	if err != nil {
		log.Printf("usenet password discover: prowlarr: %v", err)
		return nil
	}
	titles := make([]string, 0, len(releases))
	for _, r := range releases {
		titles = append(titles, r.Title)
	}
	pw := usenet.PasswordsFromTitles(titles)
	if len(pw) > 0 {
		log.Printf("usenet password discover: indexer titles yielded %d candidate(s)", len(pw))
	}
	return pw
}

func passwordsFromNativeSearch(ctx context.Context, query string) []string {
	svc := getNNTPNativeService()
	if svc == nil {
		return nil
	}
	cands, err := svc.Search(ctx, usenetsearch.Query{
		Terms: usenetsearch.NormalizeTitleTerms(query),
		Limit: 20,
	})
	if err != nil {
		log.Printf("usenet password discover: native: %v", err)
		return nil
	}
	titles := make([]string, 0, len(cands))
	for _, c := range cands {
		titles = append(titles, c.Name)
		for _, f := range c.Files {
			titles = append(titles, f.Subject, f.Filename)
		}
	}
	pw := usenet.PasswordsFromTitles(titles)
	if len(pw) > 0 {
		log.Printf("usenet password discover: native index yielded %d candidate(s)", len(pw))
	}
	return pw
}

func passwordFromWebAI(ctx context.Context, search websearch.Client, ai identify.AIClient, query string) []string {
	if search == nil || ai == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	results, err := search.Search(ctx, query+" nzb rar password", 5)
	if err != nil || len(results) == 0 {
		return nil
	}
	pw := extractArchivePassword(ctx, ai, query, results)
	if pw == "" {
		return nil
	}
	log.Printf("usenet password discover: AI web search yielded 1 candidate")
	return []string{pw}
}

func extractArchivePassword(ctx context.Context, ai identify.AIClient, query string, results []websearch.Result) string {
	if ai == nil || len(results) == 0 {
		return ""
	}
	var snippets strings.Builder
	for i, r := range results {
		if i > 0 {
			snippets.WriteString("\n\n")
		}
		fmt.Fprintf(&snippets, "Result %d:\nTitle: %s\nSnippet: %s\nURL: %s", i+1, r.Title, r.Description, r.URL)
	}
	prompt := fmt.Sprintf(`You extract an archive password that is EXPLICITLY stated for this Usenet/NZB release.

Release name: %q

Web search results:
%s

Rules (follow strictly):
1. Respond with ONLY JSON: {"password":"..."} or {"password":null}.
2. The password must appear explicitly in a result ({{pw}}, password=, "password is X", an NZB comment).
3. If nothing explicit is stated, respond {"password":null}. Never invent. Never use the release group, the word "password", or a guess from the filename.

Prefer declining over a wrong password.`, query, snippets.String())

	resp, err := ai.ChatJSON(ctx, prompt)
	if err != nil {
		return ""
	}
	pw := strings.TrimSpace(ollama.NormalizeField(resp["password"]))
	if pw == "" || strings.EqualFold(pw, "password") || len(pw) > 128 {
		return ""
	}
	return pw
}

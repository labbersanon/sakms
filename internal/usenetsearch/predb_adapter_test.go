package usenetsearch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/usenet"
	"github.com/labbersanon/sakms/internal/usenetsearch/predb"
)

func usenetBodyNFO(name string) usenet.BodySample {
	data := []byte("  RELEASE INFO\n  Name: " + name + "\n  Size: 4GB\n")
	return usenet.BodySample{Data: data, WireBytes: int64(len(data))}
}

// An md5-subject set resolves from the local corpus (fed by IRC/HTTP) with zero
// BODY fetches, through the real PreDBService + Store.
func TestNameFix_Md5SubjectResolvesFromLocalPreDBStore(t *testing.T) {
	const title = "Corpus.Movie.2019.2160p.WEB-DL.x265-GRP"
	h, _ := predb.HashesOf(title)
	f := newNFFixture(t, obfuscatedSet("p", h, 1, 45), nil)
	store := predb.NewStore(f.idx.db)
	if err := store.Upsert(context.Background(), predb.Entry{Title: title}, predb.SourceIRC); err != nil {
		t.Fatal(err)
	}
	f.nf.PreDB = &PreDBService{Store: store}
	f.crawlAndRecompute(t)
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.HashHits != 1 || res.Renamed != 1 || len(f.src.bodyCalls) != 0 {
		t.Fatalf("result = %+v bodyCalls=%v", res, f.src.bodyCalls)
	}
	if _, _, display, _ := f.release(t); display != title {
		t.Fatalf("display = %q", display)
	}
}

func TestNameFix_MatchPrefilesExactDumpFilename(t *testing.T) {
	const title = "Dump.File.2020.1080p.BluRay.x264-GRP"
	const fn = "v1aaqzdh11lfeaan9yuztj.part01.rar"
	rows := leadTokenHashSet("df", "KLoSR1zOTNoaex3x", "v1AAqzDH11LfEAAN9yuzTJ", 1, 45)
	f := newNFFixture(t, rows, nil)
	store := predb.NewStore(f.idx.db)
	if err := store.Upsert(context.Background(), predb.Entry{Title: title, Filename: fn}, predb.SourceDump); err != nil {
		t.Fatal(err)
	}
	f.nf.PreDB = &PreDBService{Store: store}
	f.crawlAndRecompute(t)
	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_releases SET next_name_at=?`, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.HashHits != 1 || res.Renamed != 1 || res.BodyFetches != 0 {
		t.Fatalf("exact predb.filename join must name before BODY: %+v", res)
	}
	if _, _, display, _ := f.release(t); display != title {
		t.Fatalf("display = %q", display)
	}
}

func TestPreDBService_HTTPHitCachesAndMissIsCached(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("q") == "Known.Movie.2021.1080p.BluRay.x264-GRP" {
			fmt.Fprint(w, `{"status":"success","data":[{"release":"Known.Movie.2021.1080p.BluRay.x264-GRP","size":8000,"files":60,"section":"X264-HD","pretime":1,"status":0}]}`)
			return
		}
		if r.URL.Query().Get("q") == "Broken.Movie.2021.1080p.BluRay.x264-GRP" {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"status":"success","results":0}`)
	}))
	defer srv.Close()
	f := newNFFixture(t, obfuscatedSet("q", nfHash, 1, 2), nil)
	hc := predb.NewHTTPClient(srv.URL)
	hc.Interval = time.Millisecond
	svc := &PreDBService{Store: predb.NewStore(f.idx.db), HTTP: hc}
	ctx := context.Background()

	hit, ok := svc.ByName(ctx, "Known.Movie.2021.1080p.BluRay.x264-GRP")
	if !ok || hit.SizeBytes != 8000<<20 {
		t.Fatalf("hit = %+v ok=%v", hit, ok)
	}
	if _, ok := svc.ByName(ctx, "Known.Movie.2021.1080p.BluRay.x264-GRP"); !ok || calls.Load() != 1 {
		t.Fatalf("second lookup must come from the local table (calls=%d)", calls.Load())
	}
	for i := 0; i < 3; i++ {
		if _, ok := svc.ByName(ctx, "Unknown.Movie.2021.1080p.BluRay.x264-GRP"); ok {
			t.Fatal("unknown name must miss")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (miss cached after the first)", calls.Load())
	}
	for i := 0; i < 2; i++ {
		if _, ok := svc.ByName(ctx, "Broken.Movie.2021.1080p.BluRay.x264-GRP"); ok {
			t.Fatal("provider error must not rename")
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("calls = %d, want 4 (errors are not cached)", calls.Load())
	}
}

func nfoHeaderAsNFO(t *testing.T, f *nfFixture, msgid, name string) {
	t.Helper()
	f.src.bodies[msgid] = usenetBodyNFO(name)
	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_headers SET subject=REPLACE(subject, '.par2', '.nfo'), filename=REPLACE(filename, '.par2', '.nfo') WHERE msgid=?`, msgid); err != nil {
		t.Fatal(err)
	}
}

func TestNameFix_NFOSceneRenamesWithoutPreDB(t *testing.T) {
	const name = "Nfo.Movie.2020.1080p.WEB-DL.x264-GRP"
	f := newNFFixture(t, obfuscatedSet("n", nfHash, 1, 45), nil)
	f.crawlAndRecompute(t)
	nfoHeaderAsNFO(t, f, "npar2", name)
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Renamed != 1 || res.BySource[sourceNFO] != 1 {
		t.Fatalf("scene NFO must rename without PreDB: %+v", res)
	}
}

// A garbage NFO name that is not scene-shaped still needs a PreDB hit, and a
// size mismatch beyond +-15% rejects the hit.
func TestNameFix_NFOOnlyNeedsPreDBAndSizeMustMatch(t *testing.T) {
	const name = "Nfo.Movie.2020-GRP"
	f := newNFFixture(t, obfuscatedSet("n", nfHash, 1, 45), nil)
	f.crawlAndRecompute(t)
	nfoHeaderAsNFO(t, f, "npar2", name)
	ctx := context.Background()
	res, err := f.nf.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Renamed != 0 || res.Unresolved != 1 {
		t.Fatalf("NFO-only without PreDB must not rename: %+v", res)
	}

	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_releases SET next_name_at=0`); err != nil {
		t.Fatal(err)
	}
	// Payload is ~131 MB; a PreDB size of 1 GB is far outside +-15%.
	f.nf.PreDB = &fakePreDB{names: map[string]PreDBHit{name: {Title: name, SizeBytes: 1 << 30}}}
	if res, _ := f.nf.Run(ctx); res.Renamed != 0 {
		t.Fatalf("size mismatch must reject the PreDB hit: %+v", res)
	}

	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_releases SET next_name_at=0, name_attempts=0`); err != nil {
		t.Fatal(err)
	}
	f.nf.PreDB = &fakePreDB{names: map[string]PreDBHit{name: {Title: name, SizeBytes: 135 << 20}}}
	res, err = f.nf.Run(ctx)
	if err != nil || res.Renamed != 1 || res.BySource[sourcePreDB] != 1 {
		t.Fatalf("size-consistent PreDB hit should rename: %+v err=%v", res, err)
	}
}

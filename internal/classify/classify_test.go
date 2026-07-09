package classify

import "testing"

func TestFromMetadata_KidsCertification(t *testing.T) {
	for _, cert := range []string{"G", "g", "TV-Y", "TV-G", "tv-y7"} {
		r := FromMetadata(Signal{Certification: cert})
		if !r.IsKids || !r.Confident {
			t.Errorf("certification %q: expected confident kids=true, got %+v", cert, r)
		}
	}
}

func TestFromMetadata_AdultCertification(t *testing.T) {
	for _, cert := range []string{"R", "NC-17", "TV-MA", "x"} {
		r := FromMetadata(Signal{Certification: cert})
		if r.IsKids || !r.Confident {
			t.Errorf("certification %q: expected confident kids=false, got %+v", cert, r)
		}
	}
}

func TestFromMetadata_PG13IsNotConfidentEitherWay(t *testing.T) {
	// PG-13/TV-14 style ratings are deliberately NOT treated as a confident
	// signal in either direction — too much ordinary family-adjacent content
	// carries these ratings for a blanket rule to be safe.
	r := FromMetadata(Signal{Certification: "PG-13"})
	if r.Confident {
		t.Errorf("PG-13 should not be confident either way, got %+v", r)
	}
}

func TestFromMetadata_RealShrekExample(t *testing.T) {
	// Shrek (2001): certification "PG", genres include Animation (and
	// others). PG alone isn't a confident kids signal, so this should defer
	// unless genres back it up with Family too — a genuinely kid-friendly
	// movie without a G rating is expected to need the AI fallback, not a
	// false negative.
	r := FromMetadata(Signal{Certification: "PG", Genres: []string{"Animation", "Comedy", "Family", "Adventure"}})
	if !r.IsKids || !r.Confident {
		t.Errorf("Shrek (PG + Animation + Family): expected confident kids=true via genre combination, got %+v", r)
	}
}

func TestFromMetadata_AnimationAloneIsNotConfident(t *testing.T) {
	// Animation alone must NOT be treated as a confident kids signal —
	// plenty of adult-oriented animation exists.
	r := FromMetadata(Signal{Genres: []string{"Animation", "Horror"}})
	if r.Confident {
		t.Errorf("Animation alone should not be confident, got %+v", r)
	}
}

func TestFromMetadata_FamilyAndAnimationTogetherIsConfidentKids(t *testing.T) {
	r := FromMetadata(Signal{Genres: []string{"Family", "Animation"}})
	if !r.IsKids || !r.Confident {
		t.Errorf("Family+Animation: expected confident kids=true, got %+v", r)
	}
}

func TestFromMetadata_ExplicitKidsGenre(t *testing.T) {
	r := FromMetadata(Signal{Genres: []string{"Kids"}})
	if !r.IsKids || !r.Confident {
		t.Errorf("explicit Kids genre: expected confident kids=true, got %+v", r)
	}
}

func TestFromMetadata_NoSignalAtAll(t *testing.T) {
	// No certification (e.g. Sonarr's series/lookup never populates this
	// field) and no relevant genres — must defer to AI, not guess in either
	// direction.
	r := FromMetadata(Signal{Genres: []string{"Drama", "Crime"}})
	if r.Confident {
		t.Errorf("no signal: expected not confident, got %+v", r)
	}
}

func TestFromMetadata_EmptySignal(t *testing.T) {
	r := FromMetadata(Signal{})
	if r.Confident {
		t.Errorf("empty signal: expected not confident, got %+v", r)
	}
}

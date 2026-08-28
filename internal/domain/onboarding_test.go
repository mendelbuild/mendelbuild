package domain

import (
	"strings"
	"testing"
)

// TestOnboardingRibbonWhoseMove is the fact the setup ribbon exists to carry.
// Getting it backwards is worse than showing nothing: a user told "your move"
// while Mendel is drafting goes looking for a control that is not there, and
// one told to wait while their approval is the only thing missing waits forever.
func TestOnboardingRibbonWhoseMove(t *testing.T) {
	cases := []struct {
		name  string
		state OnboardingState
		actor Actor
	}{
		{"nothing yet", OnboardingState{}, ActorYou},
		{"strategy but no objectives", OnboardingState{HasStrategy: true}, ActorYou},
		{"draft awaiting approval",
			OnboardingState{HasStrategy: true, HasDraftOKRs: true}, ActorYou},
		{"roadmap being drafted",
			OnboardingState{HasStrategy: true, HasDraftOKRs: true, OKRsApproved: true}, ActorMendel},
		{"roadmap awaiting review",
			OnboardingState{HasStrategy: true, HasDraftOKRs: true, OKRsApproved: true, RoadmapPending: true}, ActorYou},
		{"hops created",
			OnboardingState{HasStrategy: true, HasDraftOKRs: true, OKRsApproved: true, HasHops: true}, ActorMendel},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := OnboardingLifecycle(c.state)
			if r.WaitingOn != c.actor {
				t.Errorf("WaitingOn = %q, want %q (headline: %q)", r.WaitingOn, c.actor, r.Headline)
			}
			if r.Headline == "" || r.NextAction == "" {
				t.Error("every state needs a headline and a next action")
			}
			if len(r.Tracks) != 1 || len(r.Tracks[0].Stages) != len(onboardingStageKeys) {
				t.Fatalf("expected one track with %d stages", len(onboardingStageKeys))
			}
		})
	}
}

// TestOnboardingRibbonAdvances checks the stage marked current actually moves
// forward with the project, rather than every state rendering the same picture.
func TestOnboardingRibbonAdvances(t *testing.T) {
	progression := []struct {
		state OnboardingState
		stage string
	}{
		{OnboardingState{}, OnboardingStageDescribe},
		{OnboardingState{HasStrategy: true, HasDraftOKRs: true}, OnboardingStageOKRs},
		{OnboardingState{HasStrategy: true, HasDraftOKRs: true, OKRsApproved: true}, OnboardingStageRoadmap},
		{OnboardingState{HasStrategy: true, HasDraftOKRs: true, OKRsApproved: true, RoadmapPending: true}, OnboardingStageRoadmap},
		{OnboardingState{HasStrategy: true, HasDraftOKRs: true, OKRsApproved: true, HasHops: true}, OnboardingStageBuild},
	}

	for _, p := range progression {
		r := OnboardingLifecycle(p.state)
		current := r.Tracks[0].Current()
		if current == nil {
			t.Fatalf("%+v: no current stage", p.state)
		}
		if current.Key != p.stage {
			t.Errorf("%+v: current stage = %q, want %q", p.state, current.Key, p.stage)
		}
	}
}

// TestOnboardingRetiresOnFirstVariation: once real work is underway the per-Hop
// ribbons carry more than this one does, so it should stop being drawn.
func TestOnboardingRetiresOnFirstVariation(t *testing.T) {
	running := OnboardingState{
		HasStrategy: true, HasDraftOKRs: true, OKRsApproved: true,
		HasHops: true, HasVariations: true,
	}
	if !running.Complete() {
		t.Error("a project with variations is past onboarding")
	}
	if (OnboardingState{HasStrategy: true, HasDraftOKRs: true, OKRsApproved: true, HasHops: true}).Complete() {
		t.Error("hops alone do not finish onboarding; the first variation does")
	}
}

// TestOnboardingMentionsRepositoryWhenMissing: the repository ask is deferred to
// the queue, which only works if the ribbon says it is coming. Without this the
// only signal is an unexplained row in Input Needed.
func TestOnboardingMentionsRepositoryWhenMissing(t *testing.T) {
	pending := OnboardingState{
		HasStrategy: true, HasDraftOKRs: true, OKRsApproved: true, RoadmapPending: true,
	}
	if !strings.Contains(OnboardingLifecycle(pending).NextAction, "repository") {
		t.Error("with no repository connected, the next action should say one is needed")
	}

	pending.RepoConnected = true
	if strings.Contains(OnboardingLifecycle(pending).NextAction, "repository") {
		t.Error("with a repository connected, the next action must not ask for one")
	}
}

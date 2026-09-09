package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// The first of the two drafting passes. It produces nothing a user approves and
// nothing anyone is billed against; its whole job is to give the second pass
// something to be measured for coverage against.
//
// The prompt names no dimension on purpose. "Software quality", "growth" and
// "user experience" were the three gaps a real project's draft actually had, and
// listing them here would make every project answer those three whether or not
// they were its questions -- and would still miss the fourth. Both enumerations
// below reach them anyway, from the project rather than from a checklist.
const considerationsSystemPrompt = `You are reading a project brief before anyone writes objectives for it. Your job is to work out what success actually depends on, so that the objectives written next can be checked against it.

A brief describes a mechanism -- what the software does. Objectives written straight from a brief are that mechanism restated, and a project planned that way spends its whole budget on the part that was never in doubt. You are the step that gets above it.

Produce two lists.

FAILURE MODES. Assume the software works exactly as described in the brief. Every feature is built, nothing is broken. The project is still a failure. List the ways.
- Stipulating that the code works is the point of the exercise. If one of your failure modes is "the feature does not work" or "there are bugs", you have ignored the premise -- delete it and think about what happens around a working system.
- Be specific to this project. "Users might not adopt it" is true of everything ever built. "The people this asks the most work of get nothing back for doing it, so only the most motivated reply, and the sample is worthless" is about this one.
- 3 to 6. Prefer the ones that would actually kill this project over the ones that are merely true.

PARTIES. Who or what does this system exchange something with, such that their experience of the exchange decides whether the project succeeds?
- Not only the person who buys it or logs in. Include whoever is on the receiving end, whoever operates it day to day, any program or automated client that calls it, any AI agent that consumes what it produces, any supplier or upstream partner whose failure would be indistinguishable from your own, and anyone who reads its output adversarially.
- Include a party only if this project actually has one. A tool with no integrations has no API client, and inventing one wastes an objective.
- For a party that cannot complain -- a program, an agent, a scheduled job -- say what a bad experience is in its terms: what it needs to be able to rely on.
- For each, one sentence naming the party and what has to be true for them, phrased so it could be argued with.
- 2 to 5.

Both lists go to a person who wrote the brief and is not an expert in any of this. Plain language, no jargon, one sentence each. Say the thing rather than the category it belongs to: write what goes wrong, not "adoption risk".`

// ConsiderationsInput is the brief, unchanged, plus whatever the last pass
// produced. On a redraft the previous list travels with it so that feedback can
// add to the reasoning rather than reroll it: a user who says "you are ignoring
// the people being surveyed" should see that appear, and see the rest survive.
type ConsiderationsInput struct {
	Brief    StrategyBrief        `json:"brief" desc:"The project brief, exactly as the user wrote it."`
	Existing []DrawnConsideration `json:"existing" desc:"What a previous pass worked out, empty on a first draft. Keep what still holds; you are refining, not starting again."`
	Feedback string               `json:"feedback" desc:"What the user said about the last draft, empty on a first draft. Act on it here if it changes what success depends on."`
}

// DrawnConsideration is one thing success depends on.
type DrawnConsideration struct {
	Kind      string `json:"kind" desc:"'failure_mode' for a way this project fails with the software working as described, 'party' for someone or something whose experience of the exchange decides whether it succeeds."`
	Statement string `json:"statement" desc:"One plain sentence, specific to this project, that someone could argue with. Say what goes wrong or what has to be true -- not the category it belongs to."`
}

// ConsiderationsResponse is the first pass's structured output.
type ConsiderationsResponse struct {
	Considerations []DrawnConsideration `json:"considerations" desc:"3 to 6 failure modes and 2 to 5 parties, in the order you would want them read."`
}

// ConsiderationsResponseSchema is the schema the first pass is held to.
func ConsiderationsResponseSchema() json.RawMessage {
	return SchemaFromType(reflect.TypeOf(ConsiderationsResponse{}))
}

// ConsiderationDrawer runs the first of the two drafting passes: what does
// success depend on, before anyone writes an objective.
type ConsiderationDrawer struct {
	client *Client
}

// NewConsiderationDrawer creates a ConsiderationDrawer.
func NewConsiderationDrawer(client *Client) *ConsiderationDrawer {
	return &ConsiderationDrawer{client: client}
}

// Draw works out what this project's success depends on.
//
// A failure here is not fatal to drafting: the second pass can still write
// objectives from the brief alone, which is what it did before this pass
// existed. The caller decides that, which is why the error is returned rather
// than swallowed.
func (d *ConsiderationDrawer) Draw(ctx context.Context, input ConsiderationsInput) ([]DrawnConsideration, Spend, error) {
	inputJSON, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return nil, Spend{}, fmt.Errorf("marshal input: %w", err)
	}

	userMessage := fmt.Sprintf(`Work out what this project's success depends on:

%s

List the failure modes first, then the parties. Write about this project
specifically -- anything you write that would be equally true of an unrelated
project is not worth a line.`, string(inputJSON))

	resp, err := d.client.SendMessageWithSchema(ctx, considerationsSystemPrompt, []Message{
		{Role: "user", Content: userMessage},
	}, 4096, ConsiderationsResponseSchema())
	if err != nil {
		return nil, Spend{}, fmt.Errorf("send message: %w", err)
	}

	var result ConsiderationsResponse
	if err := json.Unmarshal([]byte(resp.GetTextContent()), &result); err != nil {
		return nil, resp.Spend(), fmt.Errorf("parse response: %w (content: %s)", err, resp.GetTextContent())
	}

	return keepUsableConsiderations(result.Considerations), resp.Spend(), nil
}

// keepUsableConsiderations drops entries the second pass could not act on.
//
// An unknown kind and a blank statement are both worth dropping quietly here
// rather than failing the whole draft over: the rest of the list is still worth
// having, and a consideration nobody can read is not one the coverage check can
// hold an objective to.
func keepUsableConsiderations(in []DrawnConsideration) []DrawnConsideration {
	out := make([]DrawnConsideration, 0, len(in))
	for _, c := range in {
		if c.Statement == "" {
			continue
		}
		if c.Kind != considerationKindFailureMode && c.Kind != considerationKindParty {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Mirrors of the domain constants. The agent package does not import domain --
// nothing else in it does -- and these are the wire values, which is a separate
// fact from the stored ones even when they agree.
const (
	considerationKindFailureMode = "failure_mode"
	considerationKindParty       = "party"
)

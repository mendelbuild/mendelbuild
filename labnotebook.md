# MendelCode Lab Notebook

Posts are in reverse chronological order.

## 2026-06-02

I spent part of today implementing a "marionette" prototype (trying to reveal ulterior motives lurking behind political speech and actions).

I haven't done as much research as I probably should, but a brief look around at "prior art" in this area seems to focus on attempts to align and/or more strictly validate individual changes (rather than evaluating a population).

There's no way I'm going to do any actual code-related things today/tonight because it's already 10pm. That being said, maybe I could use this time to brainstorm how/where I could start (tomorrow?):
* I need a basic POC of some Go code driving headless Claude Code sessions (somehow)... or maybe their equivalent?
* I should design the data structures for hierarchical selection criteria / goals / missions / etc etc etc
* I could design the queue (for humans, that is) where input is needed
* I could design the cost-budgeting pieces (both tokens and dollars)... I guess it would require an understanding of approximate percent completion of both the overall objective and the budget set to get there.

The idea would be able to describe some sort of webapp, press "Go", and then watch the system iterate until it gets there. It would be more compelling if it saw live traffic somehow... maybe there could be an AdWords component or something??

Okay... gotta turn in.

## 2026-05-26

Given that it's a prototype, I'm comfortable playing fast and loose with much of v0.1, but I do want to be deliberate and intentional about the HCI aspects.

### Thought-experiment use cases

Let's start with a few very concrete software use cases to start... we can use these to ground specific ideas. In no particular order:
* I have a longstanding bone to pick with AllTrails... there's this bug (IMO) wherein you get back to where you started the hike, get in the car, drive 20 miles, then realize you never turned off hike recording. There is a way to fix this after the fact, but it's buried and difficult. How could evolutionary software detect this antipattern and devise a solution for it? What sort of guidance would be needed, and how could it figure out which option is best?
* When Toast (the cat) got sick, I had Claude build a static XLSX model of cellular availability of steroids based on cat weight and daily dosing information. How could I end up with software that does all of this and more to track his wellness and medication/food/etc? Ideally it would be visible to vets or other trusted parties.
* MC's use case: software to handle state-by-state integrations of very legacy and very janky public IT systems and databases (judicial, police, prisons, etc)... there's not enough money to make it into a market but they have a real need and there are real social benefits.
* I just let a plumber in to install a part that could fix our hot water heater (it worked!). We probably would have discovered the issue before the hot water ran cold if we'd been regularly servicing the appliance, but our plumber has no marketing software to remind clients about service appts. There are a few SaaS vendors that work with service pros like plumbers to automate this sort of thing, but our plumber was daunted by the integration. How can software "evolve" to solve his specific problems, integrating only with what's needed, and only charge the infra/hosting fees? (I found things like Jobber online, but it's silly that it costs $2K/year to manage annual customer text messages)
* Lightstep's pivot to support metrics: everything from initial product ideation to tech selection, with the caveat that I fully expect humans to be entirely necessary (so how to integrate them without abandoning the evolutionary paradigm).

### Initial ideas/constraints

What are things that I want in an evolutionary software dev system? Again in no particular order:
* Total trust and confidence in invariants the builder specifies (semantic, cost, etc)
* Control over debt vs velocity
* Capability to inspect and ask questions; detailed records by default
* Strong bias towards experimental evidence vs prediction (even for things that seem "obvious")
* Hierarchical tiering of "runs" (evolutionary experiments)
* Tighter coupling between source control branches and experiments running (potentially against live traffic)
* [Maybe] Explicit understanding of stateful vs stateless consequences... i.e., understanding the dangers of competing experiments making competing or even "one-way" changes to storage systems
* Thoughtful queueing around the human(s)... different queues for different expertise, scheduling uncontroversial work when humans are blocking everything else, etc
* Budgets everywhere: tokens, infra costs, user errors, etc
* Ideally the AI realizes its own limits and only bites of chunks it can manage on its own... not sure how to delineate those boundaries, though
* SLOs as first-class citizens

### Where to start?

Current Claude Code loop would be to take something a bit like this lab notebook and ask it to "go build the thing"... it would come back with a design doc ("plan") to review, and after approving we'd have a pile of code.

What I want to do is provide a list of constraints or invariants and then have multiple credible (and distinct) options to evaluate within those constraints. Coming up with good constraints and invariants is hard! Especially errors of omission. But I'm fine doing that hard work if it means not having to do all of the coding, and also if it means not chasing Claude Code in multiple terminals at once.

I also would prefer to provide these constraints (or goals, or fitness functions, ...) in a structured format rather than an English paragraph.

I guess these individual runs/revs are "the new pull request", with the lifetime of the run extending well beyond review and CI testing into something way further down the pipe. They can also branch out and have "children" to evaluate.

If the only thing that Mendel does is to manage all of that complexity, it would be an improvement over Claude Code, at least given my priorities. And I guess each change could have some sort of throughline about various quality metrics, too (code smells, telemetry, cost estimates, latency, etc).

So: when I get back to this, I need to think through what that might look like... how to handle the variations and UX exploration, and how to handle the queue of necessary decisions.


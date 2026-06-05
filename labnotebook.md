# MendelBuild Lab Notebook

Posts are in reverse chronological order.

## 2026-06-05

Given the below, let's now think through a few scenarios... (note: originally started on this yesterday but didn't get far)

#### Determining Roadmaps

- Human+Agent can collaborate, or the agent can try to DIY and put the Roadmap in the queue.
- The goal is not to "fill up" the roadmap, but rather to advance the strategy
- Roadmap needs to be populated with Hops (incl Budgets!), incl HopGoals, though the Variations come later
- Entire Roadmap enters decision queue
- Needs to be a mechanism to push back on inadequate budget / assess feasibility

#### Life-of-a-Hop

- Hop is defined, likely by an AI with human review or approval, including one or more HopGoals
- The HopGoals need to apply apples-to-apples across the variations and should be decided first
- If we start with "EPD Hops" (combo of eng, product, design), I guess we can ask for multiple designs, and possibly also multiple implementations that optimize for different things (code brevity vs performance vs minimal dependencies)
  - Note that this would extend well to the idea of sub-hops... one for Product+Design, then further dependent Hops for eng implementation
- There are three Decisions per variation: (1) whether to bother implementing, (2) whether to promote to recv live traffic, and then (3) whether it *could* be selected as "the" Variation to merge back to `main`
- Humans should weigh in on every promotion to prod as well as the `main` merge... eventually some of this could be done by an agent, but not until there's more data to train on
- We would need a cost estimate for each Budgeted quantity before every stage of this process, including the exploration of Variations. I guess initially there can just be static models for some of these things, though eventually it could be a RL-trained service that provides an IQR (or similar) for each type of activity and budget type
  - Obviously we wouldn't bother creating more variations than we could ever have budgets to create and run
- We could watch the system churning away on the various tasks, or move over to the Queue to review/approve/deny pieces as they come in

#### Handling schema changes with different variations

From the very first explorations of these ideas, I've been concerned about schema changes that are "Hop-specific"... how to handle?

One idea would be to just embrace the idea that there's a lot of detritus in the datastore and to be resilient to extra/vestigial data in Variations that don't know what to do with something.

Much (much) harder to handle missing data that's expected or needed! I think (?) the best option would be to get code checked into `main` that removes the dependency on the thing to be changed/removed; then WAIT for every Variation that expects it to be there to terminate; then to actually remove the dependency in code; and then finally to drop the data itself.

For field additions, I guess there "just" need to be destructors/finalizers for Variations that are killed to drop (and maybe backup) the data they added that's no longer needed.

I must admit that the database will get very messy very quickly in this world... but I also don't know what to do to get around that.


## 2026-06-04

I finally have a day to devote to this project but am having trouble getting out of writer's block (again). I also have a cat that refuses to get off my lap and a tweaked back that's creating ergonomic problems with typing.

Anyway, I felt excited about the convergence of Hops, Queues, Roadmaps, and OKRs/Strategy into a single coherent system. I guess theoretically the "Roadmap" could become just a "type of Hop" (alongside design, coding, etc). Actually, I guess (?) that OKRs could also be subjected to some sort of population modeling, though that makes my head spin.

Would it be fair to say that all Roadmap items are "Hops" of some kind? Maybe that would help to formalize and standardize the dependencies between Hops as well as the uncertainty/cost associated with various potential outcomes.

Trying again at establishing key primitives (soon I will be doing this in some sort of more formal way... Go or SQL or whatever):
- Project
  - Id
  - Strategy
  - List of Ecosystem
  - List of Repository
  - (Credentials?)
- Repository
  - Id
  - Type (code, design, marketing, etc)
  - Function to get a interface to the repo at a given commit/guid
- Strategy
  - Id
  - Objective
  - KRs: List of KeyResult
  - SubStrategies: List of Strategy
  - Roadmap
  - List of FundingSource
- FundingSource
  - Id
  - Type (dollars, tokens, etc)
  - Amount
  - Strategic target (i.e., before funding source dries up)
- KeyResult
  - Id
  - Rationale
  - Target
  - Time horizon requirement [i.e., some measurements require a time aggregation, perhaps even weeks/months]
  - History (timeseries)
- Roadmap
  - List: Deliverable
- Deliverable:
  - Id
  - List of Hop
  - Prereqs: List of DeliverableIds
- Ecosystems:
  - Id
  - Type (web, prod, AdWords)
  - HealthFuncs: List of HealthFunc (can be used to establish ecosystem quality KRs)
- Hop:
  - Id
  - (optional Parent Hop ptr)
  - List of HopGoal
  - Pruner: func from Variation to bool
  - Scorer: func from Variation to float (only for retained Variations)
  - RelatedKRs: List of KeyResults
  - List of Variation
  - Budgets: List of BudgetAllocation
- HopGoal:
  - Id
  - Type (KR, qualitative, quantitative)
  - Target (whether numerical or string)
  - Time horizon requirement [a la KeyResult]
- BudgetAllocation:
  - Id
  - Type (dollars, cloud $, tokens, errors?)
  - Limit
  - Spending (timeseries broken down by line item)
  - Forecast (timeseries also broken down by line item)
- Variation:
  - Id
  - Hop ptr
  - Progression: Timestamped List of VariationLifeStage (won't spell this out, but basically generation, pre-prod, prod, failed, selected)
  - Repo location (Id and commit)
  - Ecosystem location (may be null)
- DecisionQueue:
  - (all past and future Decisions, filterable and sortable in different ways)
- Decision:
  - Kind: approve roadmap change, approve Hop, promote Variations, bless Variation, etc etc etc
  - Objectivity score (the more objective, the easier to automate)
  - Importance score (some sort of estimate of how likely this is to affect higher-level goals)
  - Audit log

## 2026-06-03

I once again don't have enough time (1h) to really build anything meaningful here. Instead I thought I could sketch out some pseudocode data structures.

Strategy:
- Plain-English summary (optional)
- List of OKRs
- List of Roadmaps

OKR:
- optional parent Objective
- Plain-English Objective
- List of "Key Results": (NOTE: data may come from outside of Mendel)
  - target measurement
  - target date

Roadmap: (can have multiple roadmaps... e.g., for business, product, eng, etc?)
- DAG of Leaps
- Unlike a traditional roadmap, less about walltimes than sequencing

The Decision Queue:
- basically a SQL-style table of Decisions...
- TODO: <need more work here>

Decision:
- Kind: select exactly one, select at most one, accept/reject, rank
- Objectivity score (the more objective, the easier to automate)
- Importance score (some sort of estimate of how likely this is to affect higher-level goals)

Hop: (NOTE: this is an important term... worth getting it right. Batch? Run? Leap? Jump?)
- "Kind" (e.g., Feature, Performance, Fitness?? Not sure what words to use...)
- qualitative goal (English text)
- O/KR alignment (English? Ideally quantitative but not always realistic)
- function to convert a Variation into a Score
- List of Variations

Variation:
- <pointer to Hop>
- <pointer to RCS or equiv – GUID of variation "assets">
- list of timeseries of Hop metrics / eval criteria
- 


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


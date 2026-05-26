# MendelCode Lab Notebook

Posts are in reverse chronological order.

## 2026-05-26

Given that it's a prototype, I'm comfortable playing fast and loose with much of v0.1, but I do want to be deliberate and intentional about the HCI aspects.

Let's start with a few very concrete software use cases to start... we can use these to ground specific ideas. In no particular order:
* I have a longstanding bone to pick with AllTrails... there's this bug (IMO) wherein you get back to where you started the hike, get in the car, drive 20 miles, then realize you never turned off hike recording. There is a way to fix this after the fact, but it's buried and difficult. How could evolutionary software detect this antipattern and devise a solution for it? What sort of guidance would be needed, and how could it figure out which option is best?
* When Toast (the cat) got sick, I had Claude build a static XLSX model of cellular availability of steroids based on cat weight and daily dosing information. How could I end up with software that does all of this and more to track his wellness and medication/food/etc? Ideally it would be visible to vets or other trusted parties.
* MC's use case: software to handle state-by-state integrations of very legacy and very janky public IT systems and databases (judicial, police, prisons, etc)... there's not enough money to make it into a market but they have a real need and there are real social benefits.
* I just let a plumber in to install a part that could fix our hot water heater (it worked!). We probably would have discovered the issue before the hot water ran cold if we'd been regularly servicing the appliance, but our plumber has no marketing software to remind clients about service appts. There are a few SaaS vendors that work with service pros like plumbers to automate this sort of thing, but our plumber was daunted by the integration. How can software "evolve" to solve his specific problems, integrating only with what's needed, and only charge the infra/hosting fees? (I found things like Jobber online, but it's silly that it costs $2K/year to manage annual customer text messages)
* Lightstep's pivot to support metrics: everything from initial product ideation to tech selection, with the caveat that I fully expect humans to be entirely necessary (so how to integrate them without abandoning the evolutionary paradigm).

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
* SLOs as first-class citizens


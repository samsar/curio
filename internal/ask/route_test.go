package ask

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/samsar/curio/internal/search"
)

// TestRouteFor: made-up questions of every route, the rules' near
// misses, where two rules meet (interests before evidence, evidence before
// list), every claim cue and every list cue, and questions in other
// languages, which take the answer route.
func TestRouteFor(t *testing.T) {
	for _, tc := range []struct {
		question string
		want     Route
	}{
		// The answer route, and the near misses of the others.
		{"does postgres support json", RouteAnswer},
		{"how does raft elect a leader", RouteAnswer},
		{"did I save the article about raft last week", RouteAnswer},
		{"what did the post I bookmarked say about raft elections", RouteAnswer},
		{"how has raft changed since 2015", RouteAnswer},
		{"are there more than three kinds of tides", RouteAnswer},
		{"can you explain how raft elects a leader", RouteAnswer},
		{"which article explained consumer group rebalances", RouteAnswer},
		{"the post on sourdough hydration ratios", RouteAnswer},
		{"how long should bread cool before slicing", RouteAnswer},
		{"what is a static member in kafka", RouteAnswer},
		{"how many tides a month", RouteAnswer},
		{"", RouteAnswer},
		// The interests route: about the library as a whole.
		{"how have my interests changed", RouteInterests},
		{"what was I into in 2021", RouteInterests},
		{"what did I save most last year", RouteInterests},
		{"how many pages did I save in 2023", RouteInterests},
		{"what topics did I bookmark in March", RouteInterests},
		{"how did my reading shift over the years", RouteInterests},
		{"what have I been into lately?", RouteInterests},
		{"which areas grew the most since 2020", RouteInterests},
		{"what are my main interests now", RouteInterests},
		{"How many links did I keep each month?", RouteInterests},
		{"how many pages did I save in the last 3 months", RouteInterests},
		{"what was I into two years ago", RouteInterests},
		// The interests route: the person's saving of a topic over time.
		{"has my saving about raft changed since 2015", RouteInterests},
		{"in which year did I save the most about tides", RouteInterests},
		{"when did I stop bookmarking sourdough recipes", RouteInterests},
		{"did my interest in kafka go up or down", RouteInterests},
		{"how much stuff about knitting did I save in 2020 compared to now", RouteInterests},
		{"how has my saving of recipes shifted over the years", RouteInterests},
		{"how has what I've saved shifted since 2020", RouteInterests},
		{"has our library grown since we moved", RouteInterests},
		{"was I more into synthesizers in 2018 than now", RouteInterests},
		{"have I been saving more about woodworking lately", RouteInterests},
		{"have I saved fewer articles since the baby arrived", RouteInterests},
		// The evidence route, a cue each.
		{"is there evidence that cold showers help", RouteEvidence},
		{"any proof that standing desks work", RouteEvidence},
		{"can you prove raft is safe", RouteEvidence},
		{"what proves that tides follow the moon", RouteEvidence},
		{"has it been proven that sourdough is healthier", RouteEvidence},
		{"disprove the idea that rest days slow you down", RouteEvidence},
		{"is it true that bread must cool for an hour", RouteEvidence},
		{"I read that true that rye has no gluten", RouteEvidence},
		{"check the claim that static members never rebalance", RouteEvidence},
		{"what claims do my pages make about creatine", RouteEvidence},
		{"does anything contradict the ten thousand steps rule", RouteEvidence},
		{"what contradicts the low fat diet advice", RouteEvidence},
		{"was the spinach iron story ever debunked", RouteEvidence},
		{"help me debunk the five second rule", RouteEvidence},
		{"is the glass half full a myth", RouteEvidence},
		{"cases for and against remote work", RouteEvidence},
		{"the arguments for microservices", RouteEvidence},
		{"arguments against static typing", RouteEvidence},
		{"what would back up the claim that naps help", RouteEvidence},
		{"do my pages support the idea that sleep helps memory", RouteEvidence},
		{"does anything support the claim that cats purr to heal", RouteEvidence},
		{"how strong is the case that naps help", RouteEvidence},
		// The evidence route: a yes-or-no question that checks a claim.
		{"does stretching actually prevent injuries", RouteEvidence},
		{"is the five second rule real", RouteEvidence},
		{"is rye bread healthier than wheat", RouteEvidence},
		{"does what I saved show that naps help", RouteEvidence},
		// The list route, a cue each.
		{"everything about sourdough", RouteList},
		{"show me all my notes on kafka", RouteList},
		{"all the pages on raft consensus", RouteList},
		{"what have I saved about bike chains", RouteList},
		{"what have I saved on tides", RouteList},
		{"what do I have about static typing", RouteList},
		{"what do I have on rye bread", RouteList},
		{"give me an overview of my kafka bookmarks", RouteList},
		{"what did I save about rust last month", RouteList},
		{"what have I got on bike chains", RouteList},
		{"anything on sourdough?", RouteList},
		{"every article I have on raft", RouteList},
		{"which posts have I bookmarked about tides", RouteList},
		{"show me the links I saved about rye", RouteList},
		{"have I saved anything about raft", RouteList},
		// Where two rules meet.
		{"how many pages did I back up in 2020", RouteInterests},
		{"an overview of the evidence on intermittent fasting", RouteEvidence},
		{"is it true that everything I saved on sugar is wrong", RouteEvidence},
		// Other languages.
		{"Wie wählt Raft einen Anführer?", RouteAnswer},
		{"رافت چگونه رهبر را انتخاب می\u200cکند؟", RouteAnswer},
	} {
		assert.Equal(t, tc.want, RouteFor(tc.question), "%q", tc.question)
	}
}

// TestRoute: a request's mode forces its route; without one the words
// decide, but a question about the interests asked with a filter they
// can't take, or an order, is answered from its search.
func TestRoute(t *testing.T) {
	question := "how have my interests changed"
	assert.Equal(t, RouteInterests, Request{Question: question}.route())
	assert.Equal(t, RouteAnswer, Request{Question: question, Mode: RouteAnswer}.route())
	assert.Equal(t, RouteList, Request{Question: question, Mode: RouteList}.route())
	assert.Equal(t, RouteInterests, Request{Question: question,
		Filters: search.FilterRequest{SavedAfter: "2024-01-01"}}.route(), "the saved window it takes")
	assert.Equal(t, RouteAnswer, Request{Question: question,
		Filters: search.FilterRequest{Site: []string{"example.com"}}}.route())
	assert.Equal(t, RouteAnswer, Request{Question: question, RerankOrder: []string{"a"}}.route())
	assert.Equal(t, RouteEvidence, Request{Question: "is it true that tides follow the moon",
		Filters: search.FilterRequest{Site: []string{"example.com"}}}.route(), "other routes take any filter")
}

// TestRouteFor_Everyday: made-up everyday questions, about money, health,
// children, the home and work, that use the words rule 1 reads as saving
// or interests in their everyday sense (save, keep, read, post, interest,
// area, habit, focus, mind, bookmark), each with a change or an amount
// over time. They ask about the world or the person, not the library, so
// every one is answered from pages: the interests route would answer it
// from no page at all.
func TestRouteFor_Everyday(t *testing.T) {
	for _, question := range []string{
		// Saving money, keeping things.
		"How do I keep my sourdough starter alive for more than a year?",
		"Should I save more money this year than last year?",
		"How much did I save on taxes last year by contributing to a 401k?",
		"What's the most I can save in a Roth IRA this year?",
		"How much should I save each month for a house?",
		"How much should I save for retirement by age 40?",
		"How much should I save each month?",
		"Did I save about 15 percent more on groceries last year?",
		"When did I stop saving money for the trip?",
		"Did I save money by buying my coffee beans in bulk last year?",
		"Can I save money by switching my electricity plan this winter?",
		"Is it possible to save on my phone bill by changing plans?",
		"How do I save my progress in the game?",
		"How do I save a voicemail on my iPhone?",
		"How do I save web pages for offline reading on my laptop?",
		"Have my saving habits changed since I got married?",
		"Can I keep my old phone number if I switch carriers?",
		"Is it worth keeping my old car for another year?",
		"How long can I keep cooked rice in the fridge?",
		"How long should I keep my tax records?",
		"How long should I keep bank statements?",
		"When did I start keeping a journal?",
		// Interest, rates and bills going up.
		"Which interest rate is better for my savings account this year?",
		"Did my interest rate go up last year?",
		"Has my savings account interest gone up this year?",
		"How has the interest on my savings account changed this year?",
		"How much interest did my mortgage charge last year?",
		"How much interest will I pay on my car loan this year?",
		"Has my mortgage rate changed since I refinanced?",
		"How much has my investment into index funds grown since 2020?",
		"Did my credit score go up after I paid off the card?",
		"Why did my electricity bill go up so much last month?",
		"Why did my heating costs go up so much last month?",
		"How has the price of my favorite stock changed since I bookmarked it?",
		"How do I bookmark a page in Safari on my phone?",
		// Reading, children and health.
		"Has my reading changed my mind about nuclear power?",
		"Did I read more books this year than last?",
		"Why do I read slower now than I used to?",
		"Why has my reading speed dropped over the last few years?",
		"How much reading should my kids do each day?",
		"How many times a week should I read to my toddler?",
		"How many hours a week should my teenager spend reading?",
		"Why has my toddler stopped reading picture books?",
		"Did my daughter's reading level go up this semester?",
		"Did my son's interest in reading drop after he started school?",
		"How often should I read?",
		"How often should I read the docs before an interview?",
		"What's the most efficient way to read research papers?",
		"What is the best way to read more books every year?",
		"How much reading time does a 3000-word article take?",
		"How much protein should I eat to keep my muscles as I get older?",
		"Has my sleep changed since I started running?",
		"Has my baby's sleep changed since we started solids?",
		"Did my blood pressure go down after I cut back on salt?",
		"Has my running pace improved since I started interval training?",
		"What habits help keep my blood sugar steady during the day?",
		"How can I keep my habits going after the first month?",
		"Has my habit tracker app changed its pricing?",
		"Why do I lose interest in hobbies after a few weeks?",
		"Is it normal for my interest in food to drop when I'm sick?",
		// Focus, mind and work.
		"Has my focus changed since I started meditating?",
		"Did my focus improve after I stopped reading news in the morning?",
		"How do I keep my focus when working from home?",
		"How do I keep my focus?",
		"Has my focus at work gotten worse since I started working remotely?",
		"Has my manager's focus shifted to hiring this year?",
		"Did my team's focus change after the reorg?",
		"How do I stop my mind from racing at night?",
		"How do I clear my mind before a big presentation?",
		"What's the best way to keep my mind sharp as I age?",
		// Posts, pages and links.
		"How often should I post on my blog to grow an audience?",
		"Should I keep my old posts up after rebranding?",
		"Did I post more on social media last year than this year?",
		"How many posts did my team publish last quarter?",
		"Has my blog post traffic gone up since I changed the title?",
		"What's the most popular post on the example.com blog this year?",
		"How many pages should my thesis be?",
		"How do I keep links from breaking on my site over time?",
		"What did the article I bookmarked on Monday say about tides?",
		// The home and its area.
		"What changed in my area's zoning rules last year?",
		"Has the cost of living in my area gone up since 2020?",
		"Has my area's air quality gotten worse since 2015?",
		"What areas of my house lose the most heat in winter?",
		"Why do my houseplants keep dying in winter?",
		"How has my commute changed since we moved?",
		"How did my favorite band's sound change over the years?",
		"What topics does the example.org newsletter cover each month?",
	} {
		assert.Equal(t, RouteAnswer, RouteFor(question), "%q", question)
	}
}

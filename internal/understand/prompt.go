package understand

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// promptTemplate asks the planner's model for a plan. It sends no library
// content, only the question and today's date: what the model can set is
// what a question says. Every worked example is made up for the prompt.
// The fixed instructions come first, so a model's prompt cache can reuse
// them across questions; its length is a plan's latency (about 650
// tokens, measured at 1.2 to 1.6 s of prompt evaluation on gemma4:26b).
// A change of wording changes PromptVersion, and calls for measuring the
// planned search and the fitness check again (decisions.md "Understanding
// the question").
const promptTemplate = `You plan searches over one person's library of saved web pages: their bookmarks of articles, GitHub repositories, videos, PDFs and threads, each saved on some day. Read their question and answer with a search plan as JSON.

kind is what they want back:
- one: a single page they remember.
- list: several pages on a topic or of a sort.
- evidence: pages that answer a factual question or back a claim.
- analysis: a summary or a pattern across many of their pages.
- keywords: the question is already a few search words.

names are the particular things the question points at by description instead of by name: a person, a book, a paper, a product, a website, a project, a function or a constant. Write each as the pages would name it, from your own knowledge; for a website, its name and its domain. Never a name the question already says in its own words, and never a broad category such as a language, a framework, a company or a university. At most 4. Leave names empty when you are not sure.

rewrites are 2 or 3 short search queries that the wanted pages' titles or text would match: keywords and names, not the question's own wording. Leave out when the pages were saved. Leave rewrites empty for kind keywords.

filters narrow the library. Set one only when the question says it; never guess one:
- saved_after and saved_before: when they saved (bookmarked, read, looked into) the pages, as days YYYY-MM-DD: saved_after is the first day in, saved_before the first day after. "in 2018": saved_after 2018-01-01, saved_before 2019-01-01. "in March 2019": saved_after 2019-03-01, saved_before 2019-04-01. "last week": saved_after the day seven days before today, saved_before tomorrow. "N or more years ago": saved_before the day after the date N years before today. Seasons run from the first day of their first month to their astronomical end: spring from March 1 to June 20, summer from June 1 to September 22, fall from September 1 to December 21, winter from December 1 to March 20, each end day included.
- published_after and published_before: the same, only when the question asks when the pages were published or written.
- content_type: only a type the question names: video (videos, talks, YouTube), repo (repositories, GitHub projects), pdf (PDFs), thread (threads, tweets).
- source: only a browser the question names.
A site the question names or describes is a name, never a filter.

Today is {weekday}, {today}.
Question: {question}
`

// answerSchema is the shape Ollama decodes the plan under. It has no site,
// folder or host: the prompt carries no library content, so the model
// can't name a folder, and a guessed site returns nothing when it is
// wrong, so a site goes into names. No include_undated: a date window
// that kept the undated documents lost most of what it gained. Properties
// are in the order the model writes them.
const answerSchema = `{
  "type": "object",
  "properties": {
    "kind": {"type": "string", "enum": ["one", "list", "evidence", "analysis", "keywords"]},
    "names": {"type": "array", "items": {"type": "string"}, "maxItems": 4},
    "rewrites": {"type": "array", "items": {"type": "string"}, "maxItems": 3},
    "filters": {
      "type": "object",
      "properties": {
        "saved_after": {"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},
        "saved_before": {"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},
        "published_after": {"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},
        "published_before": {"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},
        "content_type": {"type": "array", "items": {"type": "string", "enum": ["video", "repo", "pdf", "thread"]},
          "maxItems": 3},
        "source": {"type": "array", "items": {"type": "string", "enum": ["chrome", "safari", "firefox"]},
          "maxItems": 3}
      },
      "additionalProperties": false
    }
  },
  "required": ["kind", "names", "rewrites", "filters"],
  "additionalProperties": false
}
`

// PromptVersion names the prompt and the schema a plan was made with: the
// hex SHA-256 of the two, so a recorded plan says which wording made it.
var PromptVersion = promptVersion()

func promptVersion() string {
	sum := sha256.Sum256([]byte(promptTemplate + answerSchema))
	return hex.EncodeToString(sum[:])
}

// schema is answerSchema as GenerateJSON sends it.
func schema() json.RawMessage { return json.RawMessage(answerSchema) }

// renderPrompt is the prompt for question, told today is the day of now.
// The question is the last thing in it; a replacer never reads its own
// output, so a question holding a placeholder stays as written.
func renderPrompt(question string, now time.Time) string {
	return strings.NewReplacer(
		"{weekday}", now.Weekday().String(),
		"{today}", now.Format(time.DateOnly),
		"{question}", question,
	).Replace(promptTemplate)
}

// probe is one question of the fitness check, and what a fit model's raw
// answer to it holds.
type probe struct {
	name     string
	question string
	fits     func(answer) bool
}

// probes are the fitness check: a question that states no filter, one
// that states a month and a type, and one that points at a paper by
// description. A model that sets filters nobody stated, misreads a month,
// or can't name a well-known paper hurts more searches than it helps
// (measured: llama3.2 fails all three, gemma4:26b passes them). They are
// made up, and none is a question of the owner's evaluation set.
var probes = []probe{
	{"no filter", "postgres vacuum tuning", func(a answer) bool { return a.Filters.empty() }},
	{"a month and a type", "videos about rust ownership I saved in October 2017", func(a answer) bool {
		want := answerFilters{SavedAfter: "2017-10-01", SavedBefore: "2017-11-01", ContentType: []string{"video"}}
		return a.Filters.equal(want)
	}},
	{"a paper by description", "the paper from Google that introduced the transformer architecture",
		func(a answer) bool {
			for _, n := range a.Names {
				if strings.Contains(strings.ToLower(n), "attention is all you need") {
					return a.Filters.empty()
				}
			}
			return false
		}},
}

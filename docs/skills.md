# Skills

Skills are instruction bundles that can be selected into an agent prompt or
searched through a tool. They are intentionally source-neutral. The SDK defines
the `skill.Source` interface:

```go
type Source interface {
    Skills(context.Context) ([]Skill, error)
}
```

Any application can implement this interface for databases, APIs, object
storage, config services, embedded files, or generated skills.

## Built-In Sources

- `skill.StaticSource`: in-memory skills, useful for tests and programmatic configuration.
- `skill.SourceFunc`: function adapter for custom loaders.
- `skill.MultiSource`: merges multiple sources and deduplicates named skills.
- `skill.PolicySource`: filters or rewrites skills through host policy.
- `skill.CachedSource`: wraps another source with successful-load caching.
- `skill.TimeoutSource`: bounds another source with a per-load timeout.
- `skill.PrefetchSource`: serves the last successful snapshot and refreshes stale skills in the background.
- `skill.HTTPSource`: loads skills from a JSON HTTP endpoint.
- `skill.LoadDir`: loads `SKILL.md` files from host filesystem directories.
- `skill.LoadFS`: loads `SKILL.md` files from any standard `fs.FS`, including `embed.FS`, `fstest.MapFS`, archives, or read-only directories.

`MultiSource` resolves duplicate named skills with first-source-wins semantics.
Unnamed skills are treated as anonymous instruction blocks and are all included;
they are not deduplicated.

## SKILL.md Format

`LoadDir` and `LoadFS` expect one skill per directory:

```text
skills/
  database-review/
    SKILL.md
  security-review/
    SKILL.md
```

`SKILL.md` may start with simple frontmatter:

```markdown
---
name: database-review
description: Review database migrations.
when_to_use: The task involves SQL, indexes, migrations, backfills, or rollback plans.
always_on: false
tags: database, sql, migration
---

    Check lock behavior, rollback path, data safety, and observability.
```

The portable authoring subset matches the common Agent Skills file shape:
`SKILL.md` with frontmatter `name` and `description`, followed by Markdown
instructions. That keeps skills easy to share with ecosystems that use
filesystem skill bundles.

Memax deliberately does not clone any provider-specific skill runtime. The SDK
does not assume a real filesystem, Bash tool, or VM where the model can read
additional bundled files on demand. Instead, the host owns skill loading through
`skill.Source`, and selected skill content is injected as prompt context. If an
application wants progressive file/resource loading, it should expose those
resources through normal tools.

Supported frontmatter fields:

- `name`
- `description`
- `when` or `when_to_use`
- `always_on`
- `tags`
- `policy` or `policy_hints`

`name` and `description` are the standard portable fields. The other fields are
Memax SDK extensions for local relevance selection and host policy. The parser
intentionally supports only simple `key: value` metadata and comma-separated
tags. It is not a full YAML parser.

## HTTP Format

`HTTPSource` accepts either a raw JSON array:

```json
[
  {
    "name": "database-review",
    "description": "Review database migrations.",
    "content": "Check lock behavior and rollback safety."
  }
]
```

Or an object with a `skills` array:

```json
{
  "skills": [
    {
      "name": "database-review",
      "description": "Review database migrations.",
      "content": "Check lock behavior and rollback safety."
    }
  ]
}
```

## Prompt Injection And Progressive Disclosure

Skills are treated as host-controlled configuration. Do not load untrusted
user-authored skills directly into an agent prompt without review, sandboxing,
or a host approval workflow.

The default SDK behavior is direct injection: the prompt builder selects
relevant skills and includes their full content in the named `memax.skills`
prompt part. This is simple and works well for small trusted skill sets.

For larger catalogs, set:

```go
Options{
    SkillSource:         source,
    SkillResourceSource: resources,
    SkillDisclosure:     skill.DisclosureProgressive,
}
```

Progressive mode sends only selected skill metadata in the
`memax.skill_discovery` prompt part. The SDK automatically exposes a read-only,
concurrency-safe `load_skill` tool. When the model calls `load_skill`, the full
skill body is returned as a normal tool result and persists in the session
transcript. This keeps context smaller, makes skill use auditable, and matches
the same tool-mediated capability boundary used for files, memories, and other
host-owned resources.

Progressive discovery is bounded by default. A zero-value `prompt.DefaultBuilder`
selects up to eight skills and caps the `memax.skill_discovery` prompt part at
12 KiB in progressive mode, while direct injection remains unbounded for
backward compatibility with small trusted skill sets. Hosts that want a
different catalog budget can provide a custom prompt builder with
`prompt.DefaultBuilder{SkillSelector: skill.Selector{MaxSkills: n},
SkillDiscoveryMaxBytes: bytes}`. Set `SkillDiscoveryMaxBytes` negative only when
the host has its own prompt budgeting layer.

For catalogs larger than the progressive discovery budget, register the
`toolkit/skilltools` search tool against the same source. The prompt tells the
model when metadata was omitted; `search_skills` lets it query the full catalog,
then `load_skill` can load the chosen skill by name. Search results are
metadata-only by default, including resource references but not full
instructions. Set `skilltools.Config.IncludeContent` only when the host
intentionally wants search results to include skill bodies. This keeps the
initial prompt bounded without making omitted skills unreachable.

Skills can also advertise lightweight supporting resource metadata through
`skill.ResourceRef`. When `Options.SkillResourceSource` is configured,
progressive mode exposes a read-only, concurrency-safe `read_skill_resource`
tool. The prompt lists resource names, paths, descriptions, MIME types, and size
hints, but never embeds resource content. Calling `read_skill_resource` returns
the full resource as a normal tool result with important-context retention
metadata, so large checklists, examples, templates, and schemas can stay
tool-loaded and auditable.

Progressive disclosure requires named skills. Anonymous instruction blocks are
not loadable by `load_skill`; use direct injection for those or give them stable
names.

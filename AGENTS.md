# PulseOps engineering rules (Ponytail/full)

- Build the smallest correct solution that satisfies the current milestone; prefer deletion and boring code over speculative flexibility.
- Apply YAGNI: do not add code, configuration, services, abstractions, or dependencies for unrequested future needs.
- Reuse the repository, language standard library, database constraints, browser APIs, and platform features before introducing a dependency.
- Do not create single-implementation interfaces, factories, generic repositories, or wrappers without a present, demonstrated need.
- Keep dependencies few, established, and directly justified by production requirements.
- Minimalism never excuses weak security, missing trust-boundary validation, unsafe error handling, or omitted tests for non-trivial behavior.
- Keep each change scoped to its milestone and leave one runnable check for every meaningful branch or failure mode.


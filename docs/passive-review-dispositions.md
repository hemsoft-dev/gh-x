# Passive review dispositions

For hemsoft-dev repositories, status aggregation accepts an explicit HemSoft
disposition of a passive Copilot overview when all of these hold:

- The complete review and thread connections are available.
- Copilot's latest standing current-head review is COMMENTED, has a known
  verdict, and lists a consistent set of active findings.
- Every active finding links to a Copilot thread resolved by HemSoft.
- Every thread is resolved. Each linked thread has a complete first-20 comment
  window containing a later User-authored HemSoft reply naming the full head
  SHA in backticks. That reply records the maintainer's rationale and evidence.
- The latest current-head SFL review is a later authenticated bot-authored PASS
  with matching full-head metadata.

The resolver and reply author supply disposition authority. The parser does not
decide whether prose disproves an allegation, search for dismissal keywords, or
treat a clean SFL result alone as a disposition. Maintainers remain responsible
for verifying and documenting their evidence before resolving a finding.

This marks only the latest disposed overview as non-blocking in the AI summary.
Historical reviews, thread counts, formal approvals and required checks remain
unchanged. Missing or truncated disposition inputs retain the original blocker.
Other owners keep their existing policy. ATM's separate conservative review path
does not consume these dispositions.

The public historical fixtures in src/testdata/passive-disposition preserve the
review bodies, identities, timestamps, head IDs and resolution evidence from
Chief PRs 153 and 160. They contain no local runtime state or credentials.
Neither historical PR is changed or represented as an open PR.

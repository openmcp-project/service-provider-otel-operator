# Contributing

## Code of Conduct

All members of the project community must abide by the [Open Source Code of Conduct](https://github.com/openmcp-project/.github/blob/main/CODE_OF_CONDUCT.md).
Only by respecting each other we can develop a productive, collaborative community.
Instances of abusive, harassing, or otherwise unacceptable behavior may be reported to the community leaders responsible for enforcement at [ospo@sap.com](mailto:ospo@sap.com) (SAP Open Source Program Office). All complaints will be reviewed and investigated promptly and fairly.

## Engaging in Our Project

We use GitHub to manage reviews of pull requests.

* If you are a new contributor, see: [Steps to Contribute](#steps-to-contribute)

* Before implementing your change, create an issue that describes the problem you would like to solve or the code that should be enhanced. Please note that you are willing to work on that issue.

* The team will review the issue and decide whether it should be implemented as a pull request. In that case, they will assign the issue to you. If the team decides against picking up the issue, the team will post a comment with an explanation.

## Steps to Contribute

Should you wish to work on an issue, please claim it first by commenting on the GitHub issue that you want to work on. This is to prevent duplicated efforts from other contributors on the same issue.

If you have questions about one of the issues, please comment on them, and one of the maintainers will clarify.

## Contributing Code or Documentation

You are welcome to contribute code in order to fix a bug or to implement a new feature that is logged as an issue.

The following rule governs code contributions:

* Contributions must be licensed under the [Apache 2.0 License](./LICENSE).
* Due to legal reasons, contributors will be asked to accept a Developer Certificate of Origin (DCO) when they create the first pull request to this project. This happens in an automated fashion during the submission process. SAP uses [the standard DCO text of the Linux Foundation](https://developercertificate.org/).
* Contributions must follow our [guidelines on AI-generated code](https://github.com/openmcp-project/.github/blob/main/CONTRIBUTING_USING_GENAI.md) in case you are using such tools.

## Issues and Planning

* We use GitHub issues to track bugs and enhancement requests.

* Please provide as much context as possible when you open an issue. The information you provide must be comprehensive enough to reproduce that issue for the assignee.

## Releases

Every push to `main` creates or refreshes one [draft release](https://github.com/openmcp-project/service-provider-otel-operator/releases) targeting a prepared exact-version source commit on the dedicated automation-owned `automation/release-candidate` branch. Its version and notes come from commits since the last published stable root release whose recorded source-main SHA is reachable from `main` (or whose root tag is reachable for legacy releases), not from the previous draft:

| Commit | Version increment |
|---|---|
| `feat:` / `feat(scope):` | Minor |
| `fix:`, `perf:`, and other changes | Patch |
| `type!:` / `type(scope)!:` or a `BREAKING CHANGE:` / `BREAKING-CHANGE:` footer | Major |

The largest increment wins. Keep squash-merge commit titles semantic. Before updating the draft, automation commits the release `VERSION` on the candidate source branch. Draft updates create no tags and leave source `main`, including its `VERSION`, untouched.

To release, open [GitHub Releases](https://github.com/openmcp-project/service-provider-otel-operator/releases), open the draft, review its version, notes, and prepared source, then click **Publish**. GitHub creates the root tag at the prepared source commit. The `release.published` event validates that source, creates any actual nested-module tags, and merges the exact tagged commit into latest `main`. A separate commit then changes only `VERSION` to `<released-version>-dev`. The tag stays on its original SHA, which becomes an ancestor of main; the candidate branch remains available for future drafts. Only after post-processing succeeds does the dependent `publish` job build images/OCM artifacts from the published tag source. Check both jobs before deploying. The separate Publish workflow remains manual-only for artifact reruns.

Published root tags never move. Merge conflicts and concurrent main pushes stop post-processing without changing remote main. Resolve conflicts by merging the published tag into main normally (not squash/rebase), then rerun the failed published-event workflow; an already merged release only needs its development-version commit. Completed runs are idempotent. The bot must be allowed to push to the automation-owned release-candidate branch and `main`, create merge commits on main, and create nested-module tags when applicable.

This repository has no chart of its own and no nested Go modules. Shared automation also prepares chart `version`, `appVersion`, image tags, and nested-module references in candidate source before **Publish**; those changes reach main through the release merge. Major Go module-path migrations remain source changes to prepare before publishing a major release.

`task release:patch`, `task release:minor`, `task release:major`, and `task release:prepare-dev-cycle` remain available as non-mutating guidance commands, including the current repository's releases link. `release:set-version` is still used by automation to prepare candidate source and synchronize main; it does not publish a release.

Read-only local preview (requires Node.js 22+, authenticated `gh`, and a full, current `main` checkout; no npm install needed):

```sh
node hack/common/release.cjs plan --repo openmcp-project/service-provider-otel-operator
```

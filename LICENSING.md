# Licensing keepgoing

keepgoing uses the [Sustainable Use License, Version 1.0](LICENSE), with separate commercial agreements for uses outside that license. Connor Burgess is the licensor and project steward. This guide explains the policy; it does not add restrictions to or replace the license text.

The source is public, but the commercial-use restrictions mean this is **source-available**, not OSI-approved open source. The same rules apply to everyone, including companies that make other agent tools. There is no list of prohibited competitors.

## Everyday use and commercial products

keepgoing is a supervisor that a person or a company runs for their own agent sessions.

| Use | Under the Sustainable Use License |
| --- | --- |
| Use it personally to supervise your own agent sessions | Permitted without a license fee |
| Use it inside your company to supervise your company's own agents and work | Permitted without a license fee |
| Use your own installation while consulting or doing paid client work | Permitted as your internal business use; this does not include selling customers access to keepgoing |
| Inspect or modify it for a permitted use | Permitted |
| Share a fork or copy free of charge for noncommercial purposes | Permitted with the license, notices, and a prominent notice of modifications |
| Sell or commercially redistribute keepgoing or a modified version | Requires a separate commercial agreement |
| Ship keepgoing or its implementation inside a commercial product, such as an IDE, an agent host or a managed service | Requires a separate commercial agreement |
| Sell access to keepgoing as a hosted service | Requires a separate commercial agreement |

An agent or tool that makes itself supervisable, or that calls a permitted keepgoing installation through its command line and JSON output, without incorporating or distributing keepgoing's implementation, does not by itself require a commercial agreement. The other tool's terms still apply. Using the code and other work your agents produce while keepgoing supervises them, including in paid work, is not redistribution of keepgoing's software.

A working test for the area between internal use and a service: use is permitted unless you sell a product, service or module whose commercial value derives entirely or substantially from keepgoing's functionality. This is how the license's authors read the same text for their own software (see the sources below); it is a guide to reading this license, not an addition to it.

The distinction is how the software is used or distributed, not whether its user earns money. Charging for a product or service that contains or provides keepgoing is different from running keepgoing yourself while doing paid work. If a case does not clearly fit the license, request terms before shipping it.

For commercial terms, open a [licensing inquiry](https://github.com/connorburgess/keepgoing-agents/issues/new?title=Commercial%20licensing%20inquiry) requesting a private contact. Keep confidential business details out of public issues. A commercial agreement is separate from buying the project; an inquiry does not grant permission.

## Previously published Apache-2.0 material

keepgoing was previously published under Apache-2.0. Every release through [v0.1.5](https://github.com/connorburgess/keepgoing-agents/releases/tag/v0.1.5), and every commit on `main` before this change, was published under that license. For reference, the main-branch snapshot at [`75596bf`](https://github.com/connorburgess/keepgoing-agents/tree/75596bffb92c6855d4d6965d0ac28aee0c1a0f8a), the last commit on `main` before this change at the time of writing, contains that license. Other commits, tags and branches published under Apache-2.0 retain those grants too; this reference is not an exhaustive cutoff list.

This change does **not** revoke, narrow, or charge for rights already granted under Apache-2.0. Recipients may continue using, modifying, distributing, and commercializing that material under its original terms, including in forks. A later README or license change does not remove those permissions. The original license is preserved in [LICENSES/Apache-2.0.txt](LICENSES/Apache-2.0.txt).

New material offered only under the Sustainable Use License is subject to that license. Preserve applicable Apache notices when reusing earlier material. Third-party dependencies, assets, and license documents retain their own terms; the project license does not relicense them. The terms of the software inside the executable, the Go runtime and standard library and the modules it links, are reproduced in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES), which ships in every release archive. No historical commits or releases have been rewritten.

## Contributions

New contributions follow [CONTRIBUTING.md](CONTRIBUTING.md) and the [contributor agreement](CONTRIBUTOR_LICENSE_AGREEMENT.md). Contributors retain copyright and give the steward permission to license their contributions under other terms, including commercial terms. The agreement applies only when accepted; it is not retroactive and does not convert earlier DCO sign-offs into consent.

The original [DCO 1.1](LICENSES/DCO-1.1.txt) is preserved unchanged for historical reference. Current commit sign-offs record contribution origin; they do not certify DCO 1.1 or replace explicit contributor-agreement acceptance.

## Sources of the standard terms

- The Sustainable Use License body is reproduced unchanged from [n8n's license](https://github.com/n8n-io/n8n/blob/01bd57cddf11e8173b16adce1b22a8eb94598672/LICENSE.md#sustainable-use-license). n8n-specific enterprise and branch exclusions are not part of keepgoing's policy.
- The working test for a product whose value derives from the software is the reading n8n publishes for the same license text in its [license FAQ](https://docs.n8n.io/n8n-community-license/community-license/license-faq), linked from its [explanation of the license](https://docs.n8n.io/n8n-community-license). n8n's examples and its rules about its own product are not part of keepgoing's policy.
- The short contributor agreement follows the [Indie Open Source Tiny CLA](https://github.com/indieopensource/tiny-cla/blob/v1.0.0/cla.md), also used by [n8n](https://github.com/n8n-io/n8n/blob/01bd57cddf11e8173b16adce1b22a8eb94598672/CONTRIBUTOR_LICENSE_AGREEMENT.md), with Connor Burgess named as steward.

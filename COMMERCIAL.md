# Commercial licensing

dnswizard is released under the **GNU Affero General Public License v3.0**
(AGPL-3.0-only). This page explains what that means in practice and when you
need to talk to me instead.

I am the sole copyright holder, so I can license the same code to you under
different terms. That is what this page is for.

## You do not need to ask

The AGPL is a free software licence. Under it you may, at no cost and without
asking permission:

- Run dnswizard for **any purpose**, including at work and on paid client
  engagements — penetration tests, red teams, consulting, incident response
- Run it inside your company, on any number of machines, for internal
  development, QA or CI
- Modify it for your own use
- Redistribute it, or your modified version, provided you do so under the AGPL
  and make the corresponding source available

Using the tool to do your job is not the thing this licence restricts. Security
consultants and developers are the people it was written for.

## You should talk to me

The AGPL's copyleft is what makes commercial embedding impractical. You need a
separate commercial licence if you want to:

- **Bundle dnswizard into a product you sell** — an appliance, a security
  platform, a testing suite, a distributed agent — without releasing that
  product's complete source under the AGPL
- **Offer it as a hosted or managed service** where users interact with it over
  a network, again without releasing your service's source under the AGPL
- Use it in a codebase whose licence is incompatible with the AGPL
- Obtain a warranty, indemnity, or support commitment — the AGPL provides none

Note the network clause in particular (AGPL section 13): running a *modified*
version that users reach over a network triggers the source-provision
requirement even if you never distribute a binary. Building dnswizard into a
SaaS product is exactly the case the AGPL was designed to reach.

## How to request one

Open an issue titled `commercial licence enquiry`, or email me directly. Useful
things to include:

- What you are building and how dnswizard fits into it
- Whether it ships to customers, runs as a service, or both
- Rough scale — seats, installs, or instances

I am happy to grant licences on reasonable terms, including free ones for
non-profits, educational use, and open source projects that cannot use the
AGPL. Asking costs nothing and I would rather you asked than guessed.

## Not legal advice

This page is a plain-language summary. The [LICENSE](LICENSE) file is the
document that actually governs your use, and it wins wherever the two appear to
differ.

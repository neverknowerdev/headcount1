// Builds the static landing page into dist/. No dependencies: `node build.mjs`.
import { createHash } from 'node:crypto';
import { cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';

const APP_URL = 'https://app.headcount1.ai';
const GITHUB_URL = 'https://github.com/neverknowerdev/headcount1';
const TITLE = 'headcount1 — hire an AI company';
const DESCRIPTION = 'headcount1 staffs your project with a CEO, a CTO, coders, QA, designers and marketers. They plan, build and verify the work themselves and route every step to the best-value model.';

const root = new URL('.', import.meta.url);
const read = p => readFileSync(new URL(p, root), 'utf8');
const esc = s => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
const each = (list, fn) => list.map(fn).join('\n');

// ---------- content ----------
const P = 'var(--p)', PL = 'var(--p-l)', PT = 'var(--p-t)';

const depts = [
  { lead: 'CTO', model: 'Opus 5.5', team: [['Coder', 'working'], ['QA Lead', 'idle'], ['QA', 'verifying'], ['Debugger', 'idle']] },
  { lead: 'CMO', model: 'GPT 6.1', team: [['SMM', 'idle'], ['Writer', 'working'], ['Ads Manager', 'idle']] },
  { lead: 'UX Designer', model: 'Opus 4.8', team: [['Research', 'working']] },
  { lead: 'Graphic Designer', model: 'GLM-5.3', team: [['Assets', 'idle']] },
];

const bars = [
  { name: 'Typical agent harness', sub: 'One flagship model, full history on every step', cost: '$6.40', pct: 100, delta: 'baseline', color: '#9A9486', deltaColor: 'var(--dim)' },
  { name: 'headcount1 · list prices', sub: 'Routing, lean context, escalation only on need', cost: '$2.10', pct: 33, delta: '−67%', color: PL, deltaColor: P },
  { name: 'headcount1 · auctions + Jet', sub: 'Plus inference auctions and cheapest-fit models', cost: '$0.58', pct: 9, delta: '−91%', color: P, deltaColor: P },
];

// label, left %, width %, value, color
const falls = [
  ['Typical harness', 0, 100, '100%', 'var(--ink)'],
  ['Model routing', 62, 38, '−38%', PT],
  ['Lean context + memory', 48, 14, '−14%', PT],
  ['Escalate only on need', 33, 15, '−15%', PT],
  ['Jet as judge', 23, 10, '−10%', PT],
  ['Inference auctions', 9, 14, '−14%', PT],
  ['headcount1', 0, 9, '9%', P],
];

// work type, kind, model, fit, auction price, list price
const routes = [
  ['Product decision', 'critical', 'GPT 6.1 · max', '0.97', '$4.10/M', '$10.00/M'],
  ['Architecture spec', 'deep', 'Opus 4.8', '0.95', '$0.30/M', '$15.00/M'],
  ['Frontend fix', 'code', 'GLM-5.3', '0.93', '$1.20/M', '$3.00/M'],
  ['Judge QA evidence', 'judging', 'Jet', '0.99', '$0.02/M', '$0.10/M'],
  ['Changelog copy', 'writing', 'DeepSeek V4 Flash', '0.91', '$0.06/M', '$0.30/M'],
];

const seats = [
  ['GPT 6.1', 'Passkey-only. Email recovery covers lockouts.', 'for'],
  ['Opus 5.5', 'Passkey-only, but say plainly what recovery erases.', 'for'],
  ['GLM-5.3', 'Keep a password fallback for one sprint.', 'against'],
];

// model, progress per tick (%), result, winner
const runs = [
  ['Opus 4.8', 30, '5/5 · $0.21', false],
  ['DeepSeek V4 Flash', 36, '4/5 · $0.09', false],
  ['GLM-5.3', 42, '5/5 · $0.04', true],
];

// line, tokens, kept in the brief
const ctxItems = [
  ['system · tool policy · 31 tools', '4.2k', 0], ['user: passkey login broken on Safari', '0.1k', 1],
  ['read lib/webauthn.ts (412 lines)', '9.8k', 0], ['read server/auth/*.go', '22k', 0],
  ['grep "prf" → 3 hits in webauthn.ts', '0.4k', 1], ['go test ./... output', '31k', 0],
  ['3 failing tests on iOS 18 Safari', '0.6k', 1], ['npm install log', '18k', 0],
  ['retry #2, same error', '12k', 0], ['root cause: PRF not requested at register', '0.3k', 1],
  ['browser console dump', '26k', 0], ['earlier attempts, chat turns 1–38', '23k', 0],
];

const ctxSteps = [
  ['Extract', 'DeepSeek V4 Flash pulls out the facts, errors and evidence.'],
  ['Deduplicate', 'Retries, repeated logs and dead ends are dropped.'],
  ['Recall', 'The memory system adds past decisions about this area.'],
  ['Frame', 'The brief is laid out as goal, evidence, options and the question.'],
];

const briefItems = [
  ['goal', 'Passkey sign-in works on iOS 18 Safari without breaking desktop.'],
  ['evidence', 'The PRF extension is not requested at registration, so 3 Safari tests fail. Desktop passes.'],
  ['memory', 'Earlier decision: always derive the data key from PRF, with no fallback.'],
  ['options', 'A: request PRF on every registration. B: re-enroll existing users on next sign-in.'],
  ['question', 'Pick A, B or both, and say what QA should verify.'],
];

const ctxWhy = [
  ['~50×', 'Smaller context', 'The most expensive tokens are spent only on the facts that matter. Cheaper models do the reading.'],
  ['Sharper', 'Better decisions', 'Long transcripts bury the signal. A short, structured brief keeps a smart model on the actual question.'],
  ['Faster', 'Quicker answers', 'Small prompts return in seconds, so CEO, CTO and council decisions don’t hold up the workers.'],
];

const roster = [
  ['CEO', 'Leadership', 'Owns the product outcome. Clarifies goals, sets priorities, delegates, and judges the result.'],
  ['CTO', 'Leadership', 'Owns architecture. Studies the system and writes specs with interfaces, risks and a test plan.'],
  ['CMO', 'Leadership', 'Owns positioning and go-to-market, and runs the marketing team.'],
  ['Coder', 'Engineering', 'Implements the spec in its own git worktree, inside the sandbox.'],
  ['QA Lead', 'Engineering', 'Plans verification and turns acceptance criteria into test cases.'],
  ['QA Manual', 'Engineering', 'Checks the real product in the browser, the way a user would.'],
  ['QA', 'Engineering', 'Runs automated tests and marks each spec item verified or failed.'],
  ['Debugger', 'Engineering', 'Diagnoses failures from evidence before anyone changes code.'],
  ['UX Designer', 'Design', 'Designs flows and interfaces before they are built.'],
  ['Graphic Designer', 'Design', 'Produces visual assets, brand pieces and marketing graphics.'],
  ['SMM', 'Marketing', 'Plans and drafts social content for each channel.'],
  ['Writer', 'Marketing', 'Writes posts, docs and long-form copy.'],
  ['Ads Manager', 'Marketing', 'Sets up and tunes paid campaigns.'],
];

const steps = [
  ['You', 'Drop a task', 'One line on the board, in plain language. Pick a project and a sprint, or leave it to the CEO.'],
  ['CEO', 'Define success', 'Writes the goal, context, constraints, acceptance criteria and test cases, and settles product questions itself.'],
  ['Task owner', 'Staff the work', 'Chooses the right agent and model for the type of work, briefs it precisely, and watches its progress.'],
  ['Worker', 'Build in a sandbox', 'Works in an isolated git worktree. Writes are confined to the task, and the server’s secrets are hidden from it.'],
  ['QA', 'Verify, then finish', 'Every criterion is checked and marked. Only verified work reaches done.'],
];

// note class: '' muted, 'run-c' running, 'need-c' needs the user
const board = [
  ['Backlog', '6', [['HC-151', 'Referral program landing', 'CMO', 'P3', '']]],
  ['To-do', '3', [['HC-149', 'Offline mode for drafts', 'CTO', 'P2', ''], ['HC-150', 'Launch post for Product Hunt', 'Writer', 'P2', '']]],
  ['In progress', '2', [['HC-142', 'Passkey sign-in on mobile Safari', 'Coder', '● running', 'run-c'], ['HC-146', 'Onboarding empty states', 'UX Designer', '● running', 'run-c']]],
  ['In review', '1', [['HC-139', 'Pricing page copy', 'Writer', 'needs you', 'need-c']]],
  ['Done', '24', [['HC-137', 'Push notifications', 'QA', '5/5 verified', '']]],
];

const keyChain = [
  ['01 · you', 'Your passkey', 'Face ID, Touch ID or a security key. No password exists.', false],
  ['02 · WebAuthn PRF', 'Unwrap at sign-in', 'The passkey’s PRF output unwraps your data key.', false],
  ['03 · memory only', 'Your data key', 'Lives in an in-memory keyring and is evicted when you sign out.', true],
  ['04 · at moment of use', 'Your secrets', 'API keys, MCP tokens and SSH keys are decrypted only for the outbound call.', false],
];

const security = [
  ['WebAuthn', 'Passkeys only', 'Face ID, Touch ID or a security key. No passwords are ever stored.'],
  ['Zero-knowledge', 'No master key', 'Secrets are sealed with AES-256-GCM under your key, which your passkey’s PRF unwraps at sign-in.'],
  ['Write-only API', 'Secrets never come back', 'The browser only sees has_api_key. Values are decrypted at the moment of the outbound call.'],
  ['Landlock · Seatbelt', 'Sandboxed agents', 'Agent shells can only write to their task’s workspace. The data root, database and keys stay invisible to them.'],
  ['Env scrub', 'No leaked env', 'Tokens, keys and database URLs are stripped from the environment before any agent command runs.'],
  ['Crypto-shred', 'Recovery without backdoors', 'Account recovery keeps your work and permanently destroys the old credentials.'],
];

const providers = ['OpenAI', 'Anthropic', 'Google Gemini', 'OpenAI-compatible', 'Local models', 'Model groups'];
const builtins = ['MCP servers', 'GitHub App', 'Skills', 'Sprints', 'Task relations', 'Run logs', 'Teams & invites', 'Backups'];
const planSelf = ['All 13 agent roles and custom agents', 'Unlimited companies, projects and tasks', 'SQLite or PostgreSQL', 'Passkeys, zero-knowledge vault, sandbox', 'Your own provider keys'];
const planCloud = ['Everything in self-hosted, managed for you', 'Inference auctions for every call', 'Model routing, Jet judging and automatic A/B testing', 'Shared memory system', 'No keys to manage'];

const faqs = [
  ['What is headcount1?', 'An open-source agent orchestrator. It runs a company of AI agents (CEO, CTO, CMO, coders, QA, designers, marketers) that take tasks from a board, delegate, build, verify, and report back. It ships as a single Go binary with the web UI embedded.'],
  ['How much will I be interrupted?', 'As little as possible. Only the CEO can ask you a question, and only about intent, preferences, scope or approvals. Everything else is settled between agents, and each run ends as done, in review, or blocked with one clear question.'],
  ['How does it keep token costs down?', 'Our model router sends each step to the cheapest model that fits the work, using prices set by inference auctions, and Jet judges results and makes routine decisions in place of flagship models. Stronger models come in only when a step needs them. Smart models get short, focused prompts with no long chat history, and a memory system carries context between runs.'],
  ['What are the council of experts and A/B testing?', 'For important decisions, several smart models give independent opinions and the council decides, with dissent recorded on the task. For worker tasks, headcount1 can run the same task on several models, let Jet judge the runs, keep the best verified result, and mark that model as more capable for that kind of work in future.'],
  ['Can anyone else read my data?', 'Not without your passkey. API keys, MCP tokens and SSH keys are sealed with AES-256-GCM under your personal key, which exists only in memory while you are signed in. There is no master key on the server.'],
  ['What do I need to self-host?', 'Go 1.21+ and Node 18+ to build. It runs on SQLite out of the box (~/.headcount1) or on PostgreSQL via DATABASE_URL. For multi-tenant hosts, run it on Linux with Landlock to get the full sandbox.'],
  ['Which models are supported?', 'Any provider you connect. Roles come with suggested defaults (for example GPT 6.1 for the CEO and Opus 5.5 for the CTO), and you can override them per agent or use model groups with fallbacks.'],
];

// ---------- page ----------
const star = `<a class="btn btn-dark" href="${GITHUB_URL}"><span class="star" aria-hidden="true">★</span><span>Star on GitHub</span></a>`;

const body = `
<header class="nav">
  <div class="wrap nav-in">
    <a class="logo" href="#top"><span class="logo-mark" aria-hidden="true">1</span><span>headcount1</span></a>
    <nav class="nav-links" aria-label="Sections">
      <a href="#org">Agents</a>
      <a href="#flow">Workflow</a>
      <a href="#cost">Savings</a>
      <a href="#routing">Routing</a>
      <a href="#privacy">Privacy</a>
      <a href="#pricing">Pricing</a>
      <a href="#faq">FAQ</a>
    </nav>
    ${star}
  </div>
</header>

<main>
<section id="top" class="wrap hero">
  <div class="hero-top">
    <div class="pill"><span class="dot"></span><span>Open source · self-hosted · one Go binary</span></div>
    <h1>Hire a whole company. <em class="accent">Pay for a fraction of one.</em></h1>
    <p class="hero-sub">headcount1 staffs your project with a CEO, a CTO, coders, QA, designers and marketers. They plan, build and verify the work themselves, route every step to the best-value model, and come to you only when a decision is really yours.</p>
    <div class="cta-row">
      ${star}
      <a class="btn btn-ghost" href="${APP_URL}">Launch app</a>
    </div>
  </div>

  <div class="card shadow org">
    <div class="org-bar">
      <span>acme-labs / org chart</span>
      <span><span class="dot pulse"></span>4 agents working · 0 waiting on you</span>
    </div>
    <div class="org-tree">
      <div class="ceo">
        <div class="ceo-top"><b>CEO</b><span>GPT 6.1 · max</span></div>
        <span>Owns the product. Decides, delegates, judges.</span>
      </div>
      <div class="org-stem"></div>
      <div class="depts">
${each(depts, d => `        <div class="dept">
          <div class="lead"><b>${esc(d.lead)}</b><span>${esc(d.model)}</span></div>
          <div class="team">
${each(d.team, ([name, state]) => `            <div class="member"><span>${esc(name)}</span><span class="state ${state}">${state}</span></div>`)}
          </div>
        </div>`)}
      </div>
    </div>
  </div>
</section>

<section class="pillars">
  <div class="pillars-in">
    <div class="pillar">
      <span class="eyebrow">01 — Get things done</span>
      <h2>Smart models make the calls. Every result gets checked.</h2>
      <p>Critical decisions go to the strongest reasoning models. Work follows a task workflow with acceptance criteria and test cases, and nothing counts as finished until it has been verified against them.</p>
    </div>
    <div class="pillar">
      <span class="eyebrow amber">02 — Spend less per outcome</span>
      <h2>The right model at the best price, for each step.</h2>
      <p>Model routing, inference auctions and escalation only when it's needed. Smart models get lean prompts and never carry long chat history, because a memory system holds the context for them.</p>
    </div>
  </div>
</section>

<section id="cost" class="wrap sec">
  <div class="split">
    <div class="stack">
      <span class="eyebrow">Savings</span>
      <h2 class="h-xl">Same task. <em class="accent">A tenth of the bill.</em></h2>
    </div>
    <p class="lede">A typical agent harness runs every step of a task on one flagship model and sends the whole chat history each time. headcount1 runs the same task in three ways that cost less: model routing, lean context, and buying tokens at auction.</p>
  </div>

  <div class="card shadow save">
    <div class="save-head">
      <div class="save-title">
        <b>HC-142 · Passkey sign-in fix, run three ways</b>
        <span>same spec · same acceptance criteria · illustrative figures</span>
      </div>
      <div class="save-tools">
        <div class="legend">
          <span><i></i>Plan</span>
          <span><i style="opacity:.6"></i>Build</span>
          <span><i style="opacity:.3"></i>Verify</span>
        </div>
        <button class="replay" type="button">↻ Replay</button>
      </div>
    </div>
    <div class="bars">
${each(bars, (b, i) => `      <div class="bar-row">
        <div class="bar-label"><b>${esc(b.name)}</b><span>${esc(b.sub)}</span></div>
        <div class="bar-main">
          <div class="track"><div class="fill" style="width:${b.pct}%;--c:${b.color};--d:${(i * 0.45).toFixed(2)}s"><i></i><i></i><i></i></div></div>
          <div class="cost" style="--d2:${(i * 0.45 + 1.1).toFixed(2)}s"><b>${b.cost}</b><span style="color:${b.deltaColor}">${b.delta}</span></div>
        </div>
      </div>`)}
    </div>
  </div>

  <div class="falls-wrap">
    <div class="stack" style="gap:14px">
      <h3>Where the 91% goes</h3>
      <p>Each technique takes its own share out of the typical bill. Routing and lean context work even at list prices. Jet and inference auctions take the rest.</p>
    </div>
    <div class="falls">
${each(falls, ([label, left, width, val, color], i) => {
  const edge = i === 0 || i === falls.length - 1;
  const valColor = i === falls.length - 1 ? P : i === 0 ? 'var(--ink)' : 'var(--mut)';
  return `      <div class="fall${edge ? ' strong' : ''}">
        <span>${esc(label)}</span>
        <div class="fall-track"><div class="fall-fill" style="left:${left}%;width:${width}%;background:${color};--d:${(0.6 + i * 0.18).toFixed(2)}s"></div></div>
        <span class="fall-val" style="color:${valColor}">${val}</span>
      </div>`;
})}
    </div>
  </div>
</section>

<section id="routing" class="wrap sec">
  <div class="stack" style="margin-bottom:48px;max-width:760px">
    <span class="eyebrow">Decision engine</span>
    <h2 class="h-lg">Cheap where it can be. <em class="accent">Brilliant where it counts.</em></h2>
  </div>

  <div class="router dark">
    <div class="router-head">
      <div class="stack" style="gap:10px">
        <span class="eyebrow">Model routing · Jet as judge</span>
        <h3>Each step goes to the model that fits it best for the price.</h3>
      </div>
      <p>Our router scores every model on how well it fits the type of work and on the price that clears at auction. Workers get the cheapest model that fits, Jet judges the results and makes routine decisions, and flagship models are kept for the calls that need them.</p>
    </div>
    <div class="router-body">
      <div class="r-col">
        <span class="r-cap">work type</span>
${each(routes, (r, i) => `        <div class="r-item w${i ? '' : ' on'}" data-fit="${r[3]}" data-price="${r[4]}" data-list="${r[5]}"><span>${esc(r[0])}</span><small>${r[1]}</small></div>`)}
      </div>
      <div class="r-active">
        <span>router → <span data-r="work">${routes[0][0].toLowerCase()}</span></span>
        <span class="r-model" data-r="model">${esc(routes[0][2])}</span>
        <div class="r-stats">
          <div><span class="k">fit for work type</span><span data-r="fit">${routes[0][3]}</span></div>
          <span class="fit-track"><span class="fit-fill" data-r="fitbar" style="transform:scaleX(${routes[0][3]})"></span></span>
          <div><span class="k">auction price</span><span data-r="price" style="color:var(--amber-l)">${routes[0][4]}</span></div>
          <div><span class="k">list price</span><span data-r="list" style="color:var(--d-dim);text-decoration:line-through">${routes[0][5]}</span></div>
        </div>
      </div>
      <div class="r-col">
        <span class="r-cap">chosen model</span>
${each(routes, (r, i) => `        <div class="r-item m${i ? '' : ' on'}"><span>${esc(r[2])}</span><small>${r[4]}</small></div>`)}
      </div>
    </div>
  </div>

  <div class="duo">
    <div class="card">
      <span class="eyebrow">Council of experts</span>
      <h3>Important calls are put to several smart models.</h3>
      <p>When a decision matters, several strong models weigh in independently, and the council settles on an answer. Dissent is recorded on the task.</p>
      <div class="demo council">
        <div class="ask"><small>CEO asks the council</small><span>Ship passkey-only sign-in, or keep passwords as a fallback?</span></div>
${each(seats, ([model, view, vote]) => `        <div class="seat on"><span class="seat-model">${esc(model)}</span><span class="seat-view">${esc(view)}</span><span class="vote ${vote}">${vote}</span></div>`)}
        <div class="decision on"><small>decision · 2 to 1 · dissent logged</small><span>Passkey-only, with a clear warning about what account recovery erases.</span></div>
      </div>
    </div>

    <div class="card">
      <span class="eyebrow">Automatic A/B testing</span>
      <h3>Workers compete, and the winner gets the next job.</h3>
      <p>The same task runs on several worker models at once. Jet judges the results: the run that passes verification at the lowest cost wins, and that model is marked as more capable for this kind of work next time.</p>
      <div class="demo ab done">
        <div class="demo-cap">HC-146 · Onboarding empty states · 3 parallel runs</div>
${each(runs, ([name, rate, result, win]) => `        <div class="run${win ? ' win' : ''}"><span class="run-name">${esc(name)}</span><div class="run-track"><div class="run-fill" data-rate="${rate}"></div></div><span class="run-res">${result}</span></div>`)}
        <div class="cap on"><small>capability ↑</small><span>Judged by Jet. GLM-5.3 now runs first on UI-state fixes.</span></div>
      </div>
    </div>
  </div>
</section>

<section id="smart-mode" class="wrap sec">
  <div class="split">
    <div class="stack">
      <span class="eyebrow">Smart-model mode</span>
      <h2 class="h-lg">Smart models read a brief, <em class="accent">not the transcript.</em></h2>
    </div>
    <p class="lede">Workers run in the default chat-history mode, with the full conversation, tool output and files. Before a smart model is asked anything, cheaper models condense all of that into a short brief that contains only what the decision needs.</p>
  </div>

  <div class="smart">
    <div class="card smart-card">
      <div class="smart-top"><b>Worker · chat-history mode</b><span>~148k tokens</span></div>
      <div class="ctx">
${each(ctxItems, ([text, tok, keep]) => `        <div${keep ? ' class="keep"' : ''}><span>${esc(text)}</span><span>${tok}</span></div>`)}
      </div>
      <span class="note">Purple marks the lines that make it into the brief.</span>
    </div>

    <div class="condense">
      <span class="condense-cap">condensed by cheaper models</span>
      <div class="csteps">
${each(ctxSteps, ([title, desc], i) => `        <div class="cstep"><span>${i + 1}</span><div><b>${title}</b><small>${esc(desc)}</small></div></div>`)}
      </div>
      <div class="shrink">
        <div><span>context size</span><span>148k → 3.1k</span></div>
        <div class="shrink-track"><i></i></div>
      </div>
    </div>

    <div class="smart-card dark">
      <div class="smart-top"><b>CTO · smart-model brief</b><span>~3.1k tokens</span></div>
      <div class="brief">
${each(briefItems, ([k, v]) => `        <div><small>${k}</small><span>${esc(v)}</span></div>`)}
      </div>
      <span class="brief-foot">→ Opus 5.5 · one focused call</span>
    </div>
  </div>

  <div class="cells why">
${each(ctxWhy, ([stat, title, desc]) => `    <div class="cell"><span class="stat">${stat}</span><b>${title}</b><p>${esc(desc)}</p></div>`)}
  </div>
</section>

<section id="org" class="wrap sec">
  <div class="split" style="margin-bottom:56px">
    <h2 class="h-lg">Thirteen roles,<br>one reporting line.</h2>
    <p class="lede">Each agent has a job description, a tool policy and the models that suit its work. Managers delegate; specialists execute; nobody does someone else's job. Add your own roles when you need them.</p>
  </div>
  <div class="cells roster">
${each(roster, ([name, dept, desc]) => `    <div class="cell"><div class="role-top"><b>${name}</b><span>${dept}</span></div><p>${esc(desc)}</p></div>`)}
  </div>
</section>

<section id="flow" class="wrap sec">
  <div class="stack" style="margin-bottom:56px;max-width:720px">
    <span class="eyebrow dim">How a task moves</span>
    <h2 class="h-lg">You write one line. <em>The company does the rest.</em></h2>
  </div>
  <div class="steps">
${each(steps, ([who, title, desc], i) => `    <div class="step"><div class="step-top"><span class="step-n">${i + 1}</span><span class="tag">${who}</span></div><b>${title}</b><p>${esc(desc)}</p></div>`)}
  </div>
  <div class="human">
    <div class="human-l">
      <span class="eyebrow amber">ask_human</span>
      <h3>You hear from it only when it needs you.</h3>
    </div>
    <div class="human-r">
      <p>Only the CEO can interrupt you, and only about things you alone can answer: what you intended, what you prefer, how far the scope goes, and whether something is approved. Coders, QA and task owners settle the rest among themselves.</p>
      <p>Every run ends in one of three states: <strong>done</strong>, <strong>in review</strong>, or <strong>blocked</strong> with a clear question attached.</p>
    </div>
  </div>
</section>

<section class="wrap sec product">
  <div class="card shadow board-card">
    <div class="board-bar">
      <div class="crumbs"><b>Acme Labs</b><i>/</i><span>Mobile app</span><i>/</i><span>Sprint 7: Launch beta</span></div>
      <span>board</span>
    </div>
    <div class="board" tabindex="0" role="group" aria-label="Example task board">
${each(board, ([name, count, cards]) => `      <div class="col">
        <div class="col-top"><b>${name}</b><span>${count}</span></div>
${each(cards, ([ref, title, agent, note, cls]) => `        <div class="task"><span class="task-ref">${ref}</span><span class="task-title">${esc(title)}</span><div class="task-foot"><span>${agent}</span><span${cls ? ` class="${cls}"` : ''}>${note}</span></div></div>`)}
      </div>`)}
    </div>
  </div>
  <p class="caption">Companies, projects, sprints and tasks with dependencies. Every agent run is logged step by step.</p>
</section>

<section id="privacy" class="privacy dark">
  <div class="wrap privacy-in">
    <div class="split">
      <h2 class="h-xl">Your data opens with your passkey. <em>Nothing else opens it.</em></h2>
      <p class="lede">There are no passwords and no master key on the server. Your encryption key is unwrapped in memory when your passkey signs you in and dropped when you sign out. Anyone who takes the server while you're signed out gets only ciphertext.</p>
    </div>
    <div class="chain">
${each(keyChain, ([step, title, desc, hi]) => `      <div class="key${hi ? ' hi' : ''}"><small>${step}</small><b>${title}</b><span>${esc(desc)}</span></div>`)}
    </div>
    <div class="cipher">
      <span>on disk, in backups, in the DB</span>
      <span>enc:u1:42:Zm9yZ2V0LWl0Lm5vLW1hc3Rlci1rZXk7Hq2xVb…</span>
      <span>ciphertext only</span>
    </div>
    <div class="cells sec-grid">
${each(security, ([tag, title, desc]) => `      <div class="cell"><small>${tag}</small><b>${title}</b><p>${esc(desc)}</p></div>`)}
    </div>
  </div>
</section>

<section class="wrap sec">
  <div class="integ">
    <div class="stack">
      <span class="eyebrow dim">Models &amp; tools</span>
      <h2 class="h-md">Any model, any tool, wired in once.</h2>
      <p>Connect providers, group models with fallbacks, and choose a default per role. You can scope MCP servers to the agents that need them, and a GitHub App links repositories so every task gets its own git worktree.</p>
    </div>
    <div class="integ-r">
      <div class="chip-group"><b>Model providers</b><div class="chips">${providers.map(p => `<span>${esc(p)}</span>`).join('')}</div></div>
      <div class="chip-group"><b>Built in</b><div class="chips fill-chips">${builtins.map(b => `<span>${esc(b)}</span>`).join('')}</div></div>
    </div>
  </div>
</section>

<section id="pricing" class="wrap sec">
  <div class="price-head">
    <h2 class="h-lg">Run it yourself, or let us run it.</h2>
    <p>Self-hosting is free. In the cloud you pay a monthly fee plus the tokens you use, priced at auction.</p>
  </div>
  <div class="plans">
    <div class="plan">
      <div class="plan-top">
        <span class="plan-name">Self-hosted</span>
        <span class="plan-price">Free</span>
        <span class="plan-sub">Open source. Bring your own API keys.</span>
      </div>
      <ul>
${each(planSelf, f => `        <li><span>${esc(f)}</span></li>`)}
      </ul>
      <a class="btn btn-outline" href="${GITHUB_URL}">★ Star on GitHub</a>
    </div>
    <div class="plan dark">
      <div class="plan-top">
        <span class="plan-name">Cloud</span>
        <span class="plan-price">$29.99<small>/mo</small> <em>+ tokens</em></span>
        <span class="plan-sub">$29.99 a month, plus the tokens you actually use at the price that clears at auction.</span>
      </div>
      <ul>
${each(planCloud, f => `        <li><span>${esc(f)}</span></li>`)}
      </ul>
      <a class="btn btn-light" href="${APP_URL}">Launch app</a>
    </div>
  </div>
</section>

<section id="faq" class="wrap sec faq">
  <h2 class="h-md">Questions</h2>
  <div class="faq-list">
${each(faqs, ([q, a], i) => `    <details name="faq"${i ? '' : ' open'}><summary>${esc(q)}</summary><p>${esc(a)}</p></details>`)}
  </div>
</section>

<section class="wrap final">
  <h2>Your first hire is a <em class="accent">CEO.</em></h2>
  <div class="cmd"><span>$</span><span>make build &amp;&amp; ./agent-orchestrator</span></div>
  <a class="btn btn-dark" href="${GITHUB_URL}">★ Star on GitHub</a>
</section>
</main>

<footer class="foot">
  <div class="wrap foot-in">
    <span>headcount1 · AI agent orchestration</span>
    <a href="${GITHUB_URL}">github.com/neverknowerdev/headcount1</a>
  </div>
</footer>`;

// ---------- output ----------
const css = read('src/styles.css').replace(/\/\*[\s\S]*?\*\//g, '').replace(/\n\s*/g, '').trim();
const js = read('src/main.js');
const jsName = `assets/main.${createHash('sha256').update(js).digest('hex').slice(0, 10)}.js`;
const fonts = 'https://fonts.googleapis.com/css2?family=Newsreader:ital,opsz,wght@0,6..72,300..600;1,6..72,300..600&amp;family=Geist:wght@400;500;600&amp;family=Geist+Mono:wght@400;500&amp;display=swap';

const html = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${TITLE}</title>
<meta name="description" content="${esc(DESCRIPTION)}">
<meta name="theme-color" content="#F6F4EF">
<meta property="og:type" content="website">
<meta property="og:title" content="${TITLE}">
<meta property="og:description" content="${esc(DESCRIPTION)}">
<meta name="twitter:card" content="summary">
<link rel="icon" href="/favicon.svg" type="image/svg+xml">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="stylesheet" href="${fonts}">
<style>${css}</style>
<script src="/${jsName}" defer></script>
</head>
<body>
${body}
</body>
</html>
`;

const dist = new URL('dist/', root);
rmSync(dist, { recursive: true, force: true });
mkdirSync(new URL('assets/', dist), { recursive: true });
cpSync(new URL('public/', root), dist, { recursive: true });
writeFileSync(new URL('index.html', dist), html);
writeFileSync(new URL(jsName, dist), js);
console.log(`dist/index.html ${(html.length / 1024).toFixed(1)} kB, ${jsName} ${(js.length / 1024).toFixed(1)} kB`);

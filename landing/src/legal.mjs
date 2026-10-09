// Content of the Terms of Service and Privacy Policy pages, rendered by build.mjs.
// A section is [heading, blocks]; a block is a paragraph (string) or a list (array).
// Strings are trusted HTML.

// The company behind headcount1, and where legal and privacy requests go.
const COMPANY = 'GMGM sp. z o.o.';
const COUNTRY = 'Poland';
// Registered address and register numbers, shown in the Contact sections.
const COMPANY_DETAILS = 'ul. Szlak 77/222, 31-153 Kraków, Poland, NIP 6762685956';
const CONTACT_EMAIL = 'legal@headcount1.ai';
const EFFECTIVE = 'October 9, 2026';

const mail = `<a href="mailto:${CONTACT_EMAIL}">${CONTACT_EMAIL}</a>`;
const who = `headcount1 is operated by ${COMPANY}, a company registered in ${COUNTRY}, referred to in this document as “headcount1”, “we” and “us”.`;
const company = `${COMPANY}${COMPANY_DETAILS ? `, ${COMPANY_DETAILS}` : `, ${COUNTRY}`}`;

export const legalPages = ({ APP_URL, GITHUB_URL }) => {
  const app = `<a href="${APP_URL}">${APP_URL.replace('https://', '')}</a>`;
  const repo = `<a href="${GITHUB_URL}">our GitHub repository</a>`;

  const terms = {
    slug: 'terms',
    title: 'Terms of Service',
    description: 'The terms that apply when you use the headcount1 website and headcount1 Cloud.',
    effective: EFFECTIVE,
    intro: `${who} These terms are the agreement between you and headcount1 for the headcount1 website and for headcount1 Cloud, the hosted service at ${app}. By creating an account or using the service you accept them. If you use the service for a company, you accept them for that company.`,
    sections: [
      ['What these terms cover', [
        'headcount1 is an agent orchestrator: it runs a company of AI agents that plan, build and verify work on the tasks you give them. You can use it in two ways.',
        [
          '<b>headcount1 Cloud</b> is the version we host. These terms apply to it and to this website.',
          `<b>Self-hosted headcount1</b> is the open-source software you build and run yourself from ${repo}. It is licensed to you under the GNU Affero General Public License v3.0 published there, or under a separate commercial license if you have agreed one with us, and these terms do not limit the rights those licenses give you. We do not operate your instance and have no access to it. That license requires you to keep the author attribution described in the repository’s NOTICE file, and it does not cover the headcount1 name and logo.`,
        ],
      ]],
      ['Your account', [
        'You must be at least 18 years old and able to enter into a contract to use the service. Give us a working email address and keep it up to date, because we use it for account recovery and service notices.',
        'Accounts have no passwords. You sign in with a passkey (Face ID, Touch ID, Windows Hello or a security key), and you are responsible for the devices and passkeys that can open your account and for everything done through it.',
        'API keys, MCP tokens and SSH keys you store are encrypted under a key that only your passkey can unlock. We hold no master key. If you lose access and recover your account by email, the account, teams, companies and tasks are kept, but the stored secrets are destroyed and you will have to enter them again. We cannot restore them.',
        'If you invite teammates, they can see and work on the companies you share with them. You are responsible for whom you invite and for what they do in your workspace.',
      ]],
      ['Agents act on your behalf', [
        'When you give headcount1 a task, its agents act for you. Depending on the task and on what you have connected, they can run shell commands in a sandbox, read and change code, create commits and pull requests, and call the tools, MCP servers and third-party services you have set up, using the credentials you provided.',
        'Agents work autonomously. They decide how to interpret a task, what to build, which models to use, what counts as verified, and when the work is done. Some decisions are made by several models together or judged by another model. All of these are automated outputs of AI models. They are not decisions, advice or statements made by headcount1.',
        'Agent output can be wrong, incomplete or insecure, and it may resemble output produced for other people. It is not legal, financial, medical or other professional advice.',
      ]],
      ['You are responsible for your agents', [
        'You are responsible for supervising your agents. That means you:',
        [
          'decide which tasks to give them and which repositories, tools, credentials and services they can reach',
          'monitor what they are doing and stop or correct them when needed',
          'set the permissions, spending limits and model settings that fit your situation',
          'review their results before you rely on them, merge them, publish them or deploy them',
        ],
        'headcount1 is not responsible for the decisions your agents make, the actions they take, or the outcomes of those decisions and actions. This includes changed or deleted code and data, failed or harmful deployments, content sent or published in your name, costs incurred at third-party services, and business decisions made on the basis of agent output. As between you and us, these are your actions and your responsibility.',
        'Do not let agents act without human review where a mistake could cause serious harm, for example in production systems, payments, or decisions that affect people’s rights, health or safety.',
      ]],
      ['Your content', [
        'Your content is what you put into the service and what the agents produce for you: tasks, prompts, files, repositories, messages and results. As between you and us, it is yours.',
        'You give us permission to host, copy, process and transmit your content only as needed to run the service for you. That includes sending the relevant parts of it to the AI model providers and integrations that carry out your tasks.',
        'We do not sell your content and we do not use it to train AI models.',
        'You confirm that you have the rights needed to submit your content and to let agents work on it, including the repositories and systems you connect.',
      ]],
      ['Models and third-party services', [
        'headcount1 works with services we do not control: AI model and inference providers, GitHub, MCP servers, and anything else you connect. Your use of them is subject to their own terms and privacy policies, and we are not responsible for how they behave, what they charge, or whether they are available.',
        'If you connect your own provider keys, that provider bills you directly for the usage your agents generate.',
      ]],
      ['Plans, fees and tokens', [
        'Paid plans of headcount1 Cloud consist of a subscription fee plus the tokens your agents use, at the prices shown on our pricing page or in the app at the time of use. Token prices are set by inference auctions and change over time.',
        [
          'The subscription fee is charged in advance for each billing period. Token usage is charged as it accrues or at the end of the period.',
          'Agents consume tokens without asking you for each call. You are responsible for all usage on your account, so use the spending limits and model settings the service gives you.',
          'Fees are exclusive of taxes, which we add where the law requires it.',
          'You can cancel at any time. Cancellation takes effect at the end of the current billing period. Fees already paid are not refunded, except where the law requires a refund.',
          'If we change the subscription price, we will tell you at least 30 days before the change applies to you.',
        ],
        'If a payment fails, we may suspend the paid features until it is settled.',
      ]],
      ['Acceptable use', [
        'You agree not to use the service, or let anyone use it through your account, to:',
        [
          'break the law, or infringe or misuse anyone else’s rights, data or systems',
          'access, probe or attack systems, accounts or repositories without authorization',
          'create or distribute malware, run phishing or fraud, or send spam',
          'generate content that is illegal, that sexually exploits minors, or that harasses or threatens people',
          'escape the agent sandbox, get around usage limits or security controls, or interfere with other customers',
          'mine cryptocurrency, or use the service’s compute for purposes unrelated to your tasks',
          'resell or provide the service to third parties without our written agreement',
          'break the terms of the model providers and integrations that your tasks run on',
        ],
      ]],
      ['The service and changes to it', [
        'headcount1 is under active development. We may add, change or remove features, and the available models and their prices change with the market. If we remove something you pay for and rely on, we will give you reasonable notice where we can.',
        'We work to keep the service available but do not promise uninterrupted or error-free operation. Keep your own copies of anything important, such as your code in your own git remote.',
      ]],
      ['Suspension and termination', [
        'You can stop using the service and delete your account at any time.',
        'We may suspend or close an account that breaks these terms, puts the service or other people at risk, or has unpaid fees. Unless the situation is urgent, we will warn you first and give you a chance to fix it.',
        'When an account is closed, your right to use the service ends and we delete your content as described in the <a href="/privacy">Privacy Policy</a>. Sections that by their nature should survive, such as fees owed, disclaimers and limits of liability, continue to apply.',
      ]],
      ['Disclaimers', [
        'The service is provided “as is” and “as available”. To the extent the law allows, we disclaim all warranties, express or implied, including merchantability, fitness for a particular purpose and non-infringement. We do not warrant that agent decisions, actions or output are accurate, safe, lawful or fit for your purpose, or that agents will complete a task or complete it the way you intended.',
      ]],
      ['Limitation of liability', [
        'To the extent the law allows, we are not liable for indirect, incidental, special, consequential or punitive damages, or for lost profits, revenue, data or goodwill, including damage caused by decisions, actions or output of AI agents and models.',
        'Our total liability for all claims relating to the service is limited to the greater of the amount you paid us in the 12 months before the claim arose and US$100.',
        'Nothing in these terms limits liability that cannot be limited by law, or rights you have as a consumer that cannot be waived.',
      ]],
      ['Indemnity', [
        'If a third party brings a claim against us because of your content, the access you gave your agents, or your breach of these terms, you will cover the resulting losses and reasonable costs. This does not apply to consumers where the law forbids it.',
      ]],
      ['Changes to these terms', [
        'We may update these terms. If a change is material, we will tell you by email or in the app before it takes effect. If you keep using the service after that date, you accept the new terms. If you do not agree, stop using the service and delete your account.',
      ]],
      ['Governing law', [
        `These terms are governed by the laws of ${COUNTRY}. Disputes are decided by the courts that have jurisdiction over our registered office in ${COUNTRY}.`,
        'If you are a consumer, you keep the protection of the mandatory laws of the country where you live, and you may bring a claim in the courts of that country.',
      ]],
      ['Contact', [
        `${company}. Questions about these terms: ${mail}.`,
      ]],
    ],
  };

  const privacy = {
    slug: 'privacy',
    title: 'Privacy Policy',
    description: 'What data headcount1 collects on its website and in headcount1 Cloud, how it is used, and the choices you have.',
    effective: EFFECTIVE,
    intro: `${who} This policy explains what personal data we collect through the headcount1 website and through headcount1 Cloud, the hosted service at ${app}, what we do with it, and what choices you have.`,
    sections: [
      ['Scope', [
        `This policy covers this website and headcount1 Cloud. ${COMPANY} is the controller of the personal data described here.`,
        'It does not cover self-hosted headcount1. If you run the open-source software yourself, your data stays on your infrastructure, the software sends us no telemetry, and whoever operates that instance is responsible for the data in it.',
      ]],
      ['Data we collect', [
        'On this website we collect almost nothing. It sets no cookies and runs no analytics or advertising trackers. Like any website, our hosting provider processes your IP address and browser details to deliver the pages, and the fonts are loaded from Google Fonts, so Google receives your IP address when the page loads.',
        'In headcount1 Cloud we process:',
        [
          '<b>Account data.</b> Your email address, your team memberships, and the email addresses of people you invite.',
          '<b>Passkey data.</b> The public key and identifier of each passkey you register. Your fingerprint, face or device PIN never leaves your device and we never receive it.',
          '<b>Your content.</b> Companies, projects, tasks, comments, agent conversations and results, the files and repositories in your workspaces, and the memory the agents keep about your projects.',
          '<b>Stored secrets.</b> The API keys, MCP tokens and SSH keys you add. They are encrypted with AES-256-GCM under a key that your passkey unlocks and that exists only in memory while you are signed in. We hold no master key, so we cannot read them while you are signed out.',
          '<b>Integration data.</b> If you connect GitHub or other tools: installation and repository identifiers, your username on that service, and the events and comments those services send us for your repositories.',
          '<b>Usage and billing data.</b> Which models your agents called, how many tokens they used and what that cost. Card details are handled by our payment processor; we do not store card numbers.',
          '<b>Technical data.</b> IP address, browser type, and timestamps in server and security logs.',
          '<b>Messages you send us.</b> Support requests and other correspondence.',
        ],
      ]],
      ['Cookies', [
        'This website sets no cookies.',
        'headcount1 Cloud sets only the cookies it needs to work: session and refresh cookies that keep you signed in, a CSRF token that protects your requests, and a short-lived cookie used during account recovery. There are no advertising or cross-site tracking cookies.',
      ]],
      ['How we use data', [
        [
          'to run the service: sign you in, store your work, and carry out the tasks you give your agents',
          'to keep the service secure and to prevent abuse and fraud',
          'to measure usage and bill you for it',
          'to send service emails: account recovery links, team invitations, billing and security notices, and notices of changes to our terms',
          'to answer you when you contact us',
          'to understand, in aggregate, how the service is used so that we can fix and improve it',
          'to meet legal obligations',
        ],
        'We do not sell personal data, we do not show ads, and we do not use your content to train AI models.',
      ]],
      ['Legal bases', [
        'Where the GDPR or similar law applies, we rely on: performance of our contract with you (running the service and billing), our legitimate interests (security, abuse prevention, improving the service), legal obligations (tax and accounting records), and your consent where we ask for it.',
      ]],
      ['Who receives data', [
        [
          '<b>AI model and inference providers.</b> To carry out a step of a task, the relevant prompt and task content is sent to the model chosen for it, either the provider you connected or one selected by our router. Their handling of that data is governed by their own terms.',
          '<b>Services you connect.</b> GitHub, MCP servers and other tools receive what your agents send them when acting on your tasks.',
          '<b>Infrastructure providers.</b> Hosting and content delivery, email delivery, and payment processing, each acting on our instructions and only to the extent needed.',
          '<b>Your teammates.</b> People you invite can see the companies you share with them.',
          '<b>Authorities and counterparties.</b> When the law requires it, or when it is necessary to protect rights, safety or the security of the service.',
          '<b>A successor.</b> If the service is merged or acquired, your data may move to the new operator under terms no less protective than these.',
        ],
      ]],
      ['Retention', [
        'We keep account data and your content while your account exists. When you delete a company, a project or your account, we delete the data in it. Stored secrets are destroyed at that moment by discarding their encryption key.',
        'Copies in backups disappear as those backups rotate out. Logs are kept for a limited period for security and debugging. Billing records are kept for as long as tax and accounting law requires.',
      ]],
      ['Security', [
        'Sign-in uses passkeys only, so there are no passwords to steal. Stored secrets are encrypted per user with no master key on our servers. Agents run in a sandbox that can write only to the workspace of their task and cannot see the server’s data or keys. Traffic is encrypted in transit.',
        'No system is perfectly secure. If a breach affects your personal data, we will notify you and the authorities as the law requires.',
      ]],
      ['International transfers', [
        'We and our providers may process data in countries other than yours. When we transfer personal data out of the European Economic Area, the United Kingdom or Switzerland, we rely on a lawful transfer mechanism such as the standard contractual clauses.',
      ]],
      ['Your rights', [
        'Depending on where you live, you can ask us to give you a copy of your personal data, correct it, delete it, restrict or object to how we use it, or provide it in a portable format. You can withdraw consent at any time where we rely on it, and you can complain to your local data protection authority. In Poland that is the President of the Personal Data Protection Office (UODO).',
        `You can see and change most of your data directly in the app. For anything else, write to ${mail}. We answer within 30 days.`,
        'We do not sell or share personal data as those terms are defined in California law, and we do not treat you differently for exercising your rights.',
      ]],
      ['Children', [
        'The service is for adults. We do not knowingly collect personal data from anyone under 18. If you believe a child has given us data, tell us and we will delete it.',
      ]],
      ['Changes to this policy', [
        'We may update this policy. The date at the top shows the current version. If a change materially affects how we use your data, we will tell you by email or in the app before it takes effect.',
      ]],
      ['Contact', [
        `${company}. Privacy questions and requests: ${mail}.`,
      ]],
    ],
  };

  return [terms, privacy];
};

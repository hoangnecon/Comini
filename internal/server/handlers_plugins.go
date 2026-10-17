package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

type publicPluginDef struct {
	ID          string
	Slug        string
	DisplayName string
	Category    string
	Description string
	Keywords    []string
}

var allPublicPluginsDef = []publicPluginDef{
	// --- Developer Tools & Engineering (18) ---
	{
		ID:          "plugin_connector_1p_1a69035c238881919c4190932b2df699",
		Slug:        "github",
		DisplayName: "GitHub",
		Category:    "Developer Tools",
		Description: "Connect repositories, browse code, view and manage pull requests and issues.",
		Keywords:    []string{"git", "code", "pr", "issues", "repo", "github"},
	},
	{
		ID:          "plugin_gitlab",
		Slug:        "gitlab",
		DisplayName: "GitLab",
		Category:    "Developer Tools",
		Description: "Manage GitLab projects, review merge requests, and monitor CI/CD pipelines.",
		Keywords:    []string{"gitlab", "git", "ci", "cd", "pipelines", "mr"},
	},
	{
		ID:          "plugin_bitbucket",
		Slug:        "bitbucket",
		DisplayName: "Bitbucket",
		Category:    "Developer Tools",
		Description: "Browse Bitbucket repos, review pull requests, and manage code branches.",
		Keywords:    []string{"bitbucket", "atlassian", "git", "repo"},
	},
	{
		ID:          "code-review@openai-bundled",
		Slug:        "code-review",
		DisplayName: "Code Review",
		Category:    "Developer Tools",
		Description: "Review and manage GitHub pull requests with diff analysis and feedback.",
		Keywords:    []string{"review", "pr", "diff", "code"},
	},
	{
		ID:          "deep-research@openai-bundled",
		Slug:        "deep-research",
		DisplayName: "Deep Research",
		Category:    "Developer Tools",
		Description: "In-depth multi-source web and codebase research with automated reports.",
		Keywords:    []string{"research", "deep", "web", "analysis", "report"},
	},
	{
		ID:          "browser@openai-bundled",
		Slug:        "browser",
		DisplayName: "Browser Automation",
		Category:    "Developer Tools",
		Description: "Automate web browsing, inspect DOM elements, and extract live web data.",
		Keywords:    []string{"browser", "automation", "web", "crawl"},
	},
	{
		ID:          "chrome@openai-bundled",
		Slug:        "chrome",
		DisplayName: "Chrome Controller",
		Category:    "Developer Tools",
		Description: "Control Google Chrome tabs, interact with web pages, and scrape information.",
		Keywords:    []string{"chrome", "google", "browser", "control"},
	},
	{
		ID:          "computer-history@openai-bundled",
		Slug:        "computer-history",
		DisplayName: "Computer History",
		Category:    "Developer Tools",
		Description: "Query recent local developer activities, terminal commands, and edits.",
		Keywords:    []string{"history", "activity", "terminal", "local"},
	},
	{
		ID:          "plugin_docker",
		Slug:        "docker",
		DisplayName: "Docker",
		Category:    "Developer Tools",
		Description: "Manage containers, images, volumes, and docker-compose stacks.",
		Keywords:    []string{"docker", "containers", "images", "devops"},
	},
	{
		ID:          "plugin_kubernetes",
		Slug:        "kubernetes",
		DisplayName: "Kubernetes",
		Category:    "Developer Tools",
		Description: "Inspect pods, services, deployments, and cluster logs.",
		Keywords:    []string{"k8s", "kubernetes", "pods", "cluster", "cloud"},
	},
	{
		ID:          "plugin_postman",
		Slug:        "postman",
		DisplayName: "Postman",
		Category:    "Developer Tools",
		Description: "Execute API collections, test REST/GraphQL endpoints, and inspect responses.",
		Keywords:    []string{"api", "rest", "graphql", "postman", "http"},
	},
	{
		ID:          "plugin_sentry",
		Slug:        "sentry",
		DisplayName: "Sentry",
		Category:    "Developer Tools",
		Description: "Track application errors, view stack traces, and debug production issues.",
		Keywords:    []string{"sentry", "errors", "exceptions", "debugging", "logs"},
	},
	{
		ID:          "plugin_datadog",
		Slug:        "datadog",
		DisplayName: "Datadog",
		Category:    "Developer Tools",
		Description: "Query metrics, traces, APM dashboards, and infrastructure health.",
		Keywords:    []string{"datadog", "metrics", "apm", "monitoring", "traces"},
	},
	{
		ID:          "plugin_supabase",
		Slug:        "supabase",
		DisplayName: "Supabase",
		Category:    "Developer Tools",
		Description: "Manage Postgres tables, run SQL queries, and inspect auth logs in Supabase.",
		Keywords:    []string{"supabase", "postgres", "sql", "backend", "database"},
	},
	{
		ID:          "plugin_vercel",
		Slug:        "vercel",
		DisplayName: "Vercel",
		Category:    "Developer Tools",
		Description: "Monitor web deployments, inspect build logs, and manage domain settings.",
		Keywords:    []string{"vercel", "deploy", "nextjs", "frontend", "serverless"},
	},
	{
		ID:          "plugin_cloudflare",
		Slug:        "cloudflare",
		DisplayName: "Cloudflare",
		Category:    "Developer Tools",
		Description: "Manage DNS records, Cloudflare Workers, and CDN cache rules.",
		Keywords:    []string{"cloudflare", "dns", "workers", "cdn", "security"},
	},
	{
		ID:          "plugin_linear",
		Slug:        "linear",
		DisplayName: "Linear",
		Category:    "Developer Tools",
		Description: "Track software sprint issues, roadmaps, pull request links, and cycles.",
		Keywords:    []string{"linear", "issues", "sprint", "tracking", "bugs"},
	},
	{
		ID:          "plugin_jira",
		Slug:        "jira",
		DisplayName: "Jira Software",
		Category:    "Developer Tools",
		Description: "Search Atlassian Jira backlogs, transition tickets, and manage sprints.",
		Keywords:    []string{"jira", "atlassian", "tickets", "scrum", "agile"},
	},

	// --- Productivity & Communication (18) ---
	{
		ID:          "plugin_connector_1p_ab21a553bfbc81919ea8fd1858e3ffa7",
		Slug:        "slack",
		DisplayName: "Slack",
		Category:    "Productivity",
		Description: "Read channels, summarize discussions, and send Slack messages.",
		Keywords:    []string{"chat", "messaging", "slack", "team", "channels"},
	},
	{
		ID:          "plugin_connector_1p_eba8b52fe53881918408d4b46b957644",
		Slug:        "teams",
		DisplayName: "Microsoft Teams",
		Category:    "Productivity",
		Description: "Connect with team chats, channels, and meetings on Microsoft Teams.",
		Keywords:    []string{"chat", "meetings", "microsoft", "teams"},
	},
	{
		ID:          "plugin_discord",
		Slug:        "discord",
		DisplayName: "Discord",
		Category:    "Productivity",
		Description: "Interact with Discord community servers, channels, and announcements.",
		Keywords:    []string{"discord", "community", "gaming", "chat", "bot"},
	},
	{
		ID:          "plugin_telegram",
		Slug:        "telegram",
		DisplayName: "Telegram",
		Category:    "Productivity",
		Description: "Send and receive messages, query bot updates, and broadcast to channels.",
		Keywords:    []string{"telegram", "messaging", "chat", "bot"},
	},
	{
		ID:          "plugin_whatsapp",
		Slug:        "whatsapp",
		DisplayName: "WhatsApp Business",
		Category:    "Productivity",
		Description: "Manage customer conversations and send notifications via WhatsApp.",
		Keywords:    []string{"whatsapp", "meta", "chat", "messaging"},
	},
	{
		ID:          "plugin_zoom",
		Slug:        "zoom",
		DisplayName: "Zoom",
		Category:    "Productivity",
		Description: "Schedule video conferences, fetch meeting summaries, and transcripts.",
		Keywords:    []string{"zoom", "video", "call", "meeting", "transcript"},
	},
	{
		ID:          "plugin_connector_1p_f8509de903288191b14a160c6c5d20b0",
		Slug:        "google-calendar",
		DisplayName: "Google Calendar",
		Category:    "Productivity",
		Description: "Check schedules, manage meetings, and create calendar events.",
		Keywords:    []string{"calendar", "meeting", "events", "google", "schedule"},
	},
	{
		ID:          "plugin_connector_1p_fd0f4f41caa88191a9456514bbffa06d",
		Slug:        "outlook-calendar",
		DisplayName: "Outlook Calendar",
		Category:    "Productivity",
		Description: "Schedule and track Microsoft Outlook meetings and invites.",
		Keywords:    []string{"calendar", "outlook", "microsoft", "schedule"},
	},
	{
		ID:          "plugin_connector_1p_95d39881713c8191931482a62d6edff9",
		Slug:        "gmail",
		DisplayName: "Gmail",
		Category:    "Productivity",
		Description: "Search, summarize, and draft emails with your Gmail account.",
		Keywords:    []string{"email", "mail", "gmail", "inbox"},
	},
	{
		ID:          "plugin_connector_1p_6bcb5879c73c819196abc70016166099",
		Slug:        "outlook-email",
		DisplayName: "Outlook Email",
		Category:    "Productivity",
		Description: "Access your Outlook mailbox, view emails, and compose drafts.",
		Keywords:    []string{"email", "outlook", "mail", "inbox"},
	},
	{
		ID:          "plugin_superhuman",
		Slug:        "superhuman",
		DisplayName: "Superhuman",
		Category:    "Productivity",
		Description: "Blazing fast email triage, AI drafting, and inbox zero management.",
		Keywords:    []string{"superhuman", "email", "fast", "inbox"},
	},
	{
		ID:          "plugin_asdk_app_69a1d78e929881919bba0dbda1f6436d",
		Slug:        "notion",
		DisplayName: "Notion",
		Category:    "Productivity",
		Description: "Search workspaces, read notes, and update Notion databases and wikis.",
		Keywords:    []string{"notes", "wiki", "docs", "workspace", "notion"},
	},
	{
		ID:          "plugin_coda",
		Slug:        "coda",
		DisplayName: "Coda",
		Category:    "Productivity",
		Description: "Collaborate on interactive docs, tables, buttons, and automations.",
		Keywords:    []string{"coda", "docs", "tables", "automation"},
	},
	{
		ID:          "plugin_asana",
		Slug:        "asana",
		DisplayName: "Asana",
		Category:    "Productivity",
		Description: "Organize project tasks, track milestones, and manage team workloads.",
		Keywords:    []string{"asana", "tasks", "projects", "productivity"},
	},
	{
		ID:          "plugin_trello",
		Slug:        "trello",
		DisplayName: "Trello",
		Category:    "Productivity",
		Description: "Manage Kanban boards, move task cards, and assign team checklists.",
		Keywords:    []string{"trello", "kanban", "board", "cards"},
	},
	{
		ID:          "plugin_clickup",
		Slug:        "clickup",
		DisplayName: "ClickUp",
		Category:    "Productivity",
		Description: "All-in-one project management, docs, goals, and team reminders.",
		Keywords:    []string{"clickup", "tasks", "goals", "management"},
	},
	{
		ID:          "plugin_monday",
		Slug:        "monday",
		DisplayName: "Monday.com",
		Category:    "Productivity",
		Description: "Customizable workflows, team collaboration boards, and status tracking.",
		Keywords:    []string{"monday", "crm", "workflow", "board"},
	},
	{
		ID:          "plugin_todoist",
		Slug:        "todoist",
		DisplayName: "Todoist",
		Category:    "Productivity",
		Description: "Organize daily to-dos, set recurring due dates, and track personal goals.",
		Keywords:    []string{"todo", "tasks", "checklist", "todoist"},
	},

	// --- Documents, PDF & Office (13) ---
	{
		ID:          "documents@openai-primary-runtime",
		Slug:        "documents",
		DisplayName: "Documents",
		Category:    "Documents & Office",
		Description: "Create, view, and edit formatted rich-text documents and reports.",
		Keywords:    []string{"document", "editor", "word", "write", "notes"},
	},
	{
		ID:          "pdf@openai-primary-runtime",
		Slug:        "pdf",
		DisplayName: "PDF Viewer & Analyzer",
		Category:    "Documents & Office",
		Description: "Extract text, analyze structure, and parse PDF documents.",
		Keywords:    []string{"pdf", "reader", "extract", "adobe"},
	},
	{
		ID:          "spreadsheets@openai-primary-runtime",
		Slug:        "spreadsheets",
		DisplayName: "Spreadsheets",
		Category:    "Documents & Office",
		Description: "Analyze data, generate CSV/Excel files, and calculate formulas.",
		Keywords:    []string{"csv", "excel", "sheets", "data", "finance"},
	},
	{
		ID:          "presentations@openai-primary-runtime",
		Slug:        "presentations",
		DisplayName: "Presentations",
		Category:    "Documents & Office",
		Description: "Generate professional slide decks and presentation layouts.",
		Keywords:    []string{"slides", "pitch", "deck", "presentation", "powerpoint"},
	},
	{
		ID:          "template-creator@openai-primary-runtime",
		Slug:        "template-creator",
		DisplayName: "Template Creator",
		Category:    "Documents & Office",
		Description: "Create and scaffold starter projects, forms, and document templates.",
		Keywords:    []string{"scaffold", "template", "starter", "boilerplates"},
	},
	{
		ID:          "plugin_asdk_app_695bfc98071c8191bac7bc479aa27de7",
		Slug:        "google-drive",
		DisplayName: "Google Drive",
		Category:    "Documents & Office",
		Description: "Access docs, spreadsheets, and cloud files from your Google Drive.",
		Keywords:    []string{"drive", "docs", "files", "cloud", "google"},
	},
	{
		ID:          "plugin_google_docs",
		Slug:        "google-docs",
		DisplayName: "Google Docs",
		Category:    "Documents & Office",
		Description: "Create and collaborate on live Google Docs documents.",
		Keywords:    []string{"docs", "google", "word", "writer"},
	},
	{
		ID:          "plugin_google_sheets",
		Slug:        "google-sheets",
		DisplayName: "Google Sheets",
		Category:    "Documents & Office",
		Description: "Edit online spreadsheets, insert data rows, and build live charts.",
		Keywords:    []string{"sheets", "google", "excel", "table"},
	},
	{
		ID:          "plugin_onedrive",
		Slug:        "onedrive",
		DisplayName: "Microsoft OneDrive",
		Category:    "Documents & Office",
		Description: "Sync and browse your Microsoft OneDrive personal and business files.",
		Keywords:    []string{"onedrive", "microsoft", "files", "cloud"},
	},
	{
		ID:          "plugin_connector_1p_dca009ae2c848191ae14df3a47c5e7fd",
		Slug:        "sharepoint",
		DisplayName: "SharePoint",
		Category:    "Documents & Office",
		Description: "Access enterprise documents and team sites stored in SharePoint.",
		Keywords:    []string{"sharepoint", "microsoft", "documents", "intranet"},
	},
	{
		ID:          "plugin_box",
		Slug:        "box",
		DisplayName: "Box",
		Category:    "Documents & Office",
		Description: "Securely view and search files stored in your Box enterprise account.",
		Keywords:    []string{"box", "files", "cloud", "enterprise"},
	},
	{
		ID:          "plugin_dropbox",
		Slug:        "dropbox",
		DisplayName: "Dropbox",
		Category:    "Documents & Office",
		Description: "Share files, browse paper docs, and sync cloud folders in Dropbox.",
		Keywords:    []string{"dropbox", "cloud", "sync", "files"},
	},
	{
		ID:          "latex@openai-bundled",
		Slug:        "latex",
		DisplayName: "LaTeX Typesetting",
		Category:    "Documents & Office",
		Description: "Compile mathematical equations, scientific papers, and TeX documents.",
		Keywords:    []string{"latex", "tex", "math", "equations", "papers"},
	},

	// --- Data Analytics, Science & Research (15) ---
	{
		ID:          "wolfram@openai-primary-runtime",
		Slug:        "wolfram",
		DisplayName: "Wolfram Alpha",
		Category:    "Data & Research",
		Description: "Computational intelligence, advanced math, scientific facts, and data.",
		Keywords:    []string{"wolfram", "math", "science", "physics", "calculator"},
	},
	{
		ID:          "plugin_bigquery",
		Slug:        "bigquery",
		DisplayName: "Google BigQuery",
		Category:    "Data & Research",
		Description: "Query massive petabyte-scale data warehouses using SQL.",
		Keywords:    []string{"bigquery", "google", "sql", "warehouse", "analytics"},
	},
	{
		ID:          "plugin_snowflake",
		Slug:        "snowflake",
		DisplayName: "Snowflake",
		Category:    "Data & Research",
		Description: "Execute SQL queries, analyze data lakes, and inspect warehouse credit usage.",
		Keywords:    []string{"snowflake", "data", "warehouse", "sql", "cloud"},
	},
	{
		ID:          "plugin_tableau",
		Slug:        "tableau",
		DisplayName: "Tableau",
		Category:    "Data & Research",
		Description: "Interact with enterprise business dashboards and data visualizers.",
		Keywords:    []string{"tableau", "bi", "visuals", "dashboards", "reports"},
	},
	{
		ID:          "plugin_postgresql",
		Slug:        "postgresql",
		DisplayName: "PostgreSQL Client",
		Category:    "Data & Research",
		Description: "Connect directly to Postgres databases, inspect schemas, and query rows.",
		Keywords:    []string{"postgres", "sql", "database", "rdbms"},
	},
	{
		ID:          "plugin_mysql",
		Slug:        "mysql",
		DisplayName: "MySQL Client",
		Category:    "Data & Research",
		Description: "Manage MySQL database connections, query records, and view tables.",
		Keywords:    []string{"mysql", "sql", "database", "db"},
	},
	{
		ID:          "plugin_redis",
		Slug:        "redis",
		DisplayName: "Redis Cache",
		Category:    "Data & Research",
		Description: "Inspect Redis keys, cache entries, TTL, and pub/sub queues.",
		Keywords:    []string{"redis", "cache", "nosql", "in-memory"},
	},
	{
		ID:          "plugin_elasticsearch",
		Slug:        "elasticsearch",
		DisplayName: "Elasticsearch",
		Category:    "Data & Research",
		Description: "Full-text search, cluster health queries, and log analysis.",
		Keywords:    []string{"elastic", "search", "kibana", "logs", "fulltext"},
	},
	{
		ID:          "plugin_google_analytics",
		Slug:        "google-analytics",
		DisplayName: "Google Analytics 4",
		Category:    "Data & Research",
		Description: "Track website visitors, conversion funnels, sessions, and events.",
		Keywords:    []string{"ga4", "analytics", "traffic", "seo", "web"},
	},
	{
		ID:          "plugin_zotero",
		Slug:        "zotero",
		DisplayName: "Zotero",
		Category:    "Data & Research",
		Description: "Access academic research libraries, citations, and reference papers.",
		Keywords:    []string{"citations", "research", "papers", "zotero", "bibliography"},
	},
	{
		ID:          "plugin_google_scholar",
		Slug:        "google-scholar",
		DisplayName: "Google Scholar",
		Category:    "Data & Research",
		Description: "Search peer-reviewed papers, academic authors, and citation indices.",
		Keywords:    []string{"scholar", "academic", "papers", "science", "journal"},
	},
	{
		ID:          "plugin_pubmed",
		Slug:        "pubmed",
		DisplayName: "PubMed",
		Category:    "Data & Research",
		Description: "Search biomedical literature, life science clinical trials, and abstracts.",
		Keywords:    []string{"pubmed", "medical", "biology", "health", "clinical"},
	},
	{
		ID:          "plugin_arxiv",
		Slug:        "arxiv",
		DisplayName: "arXiv Search",
		Category:    "Data & Research",
		Description: "Query preprints across AI, computer science, physics, and mathematics.",
		Keywords:    []string{"arxiv", "preprints", "ai", "math", "physics"},
	},
	{
		ID:          "plugin_semantic_scholar",
		Slug:        "semantic-scholar",
		DisplayName: "Semantic Scholar",
		Category:    "Data & Research",
		Description: "AI-powered scientific paper discovery, citation graphs, and key findings.",
		Keywords:    []string{"papers", "ai", "citations", "semantic", "science"},
	},
	{
		ID:          "plugin_biorxiv",
		Slug:        "biorxiv",
		DisplayName: "bioRxiv & medRxiv",
		Category:    "Data & Research",
		Description: "Preprint server for biology, genomics, medicine, and epidemiology.",
		Keywords:    []string{"biorxiv", "medrxiv", "genomics", "preprints"},
	},

	// --- Design, Visuals & Media (10) ---
	{
		ID:          "plugin_figma",
		Slug:        "figma",
		DisplayName: "Figma",
		Category:    "Design & Creative",
		Description: "Inspect UI design files, extract CSS tokens, and read design comments.",
		Keywords:    []string{"figma", "ui", "ux", "design", "wireframe"},
	},
	{
		ID:          "plugin_canva",
		Slug:        "canva",
		DisplayName: "Canva",
		Category:    "Design & Creative",
		Description: "Create social media graphics, posters, and design templates with Canva.",
		Keywords:    []string{"canva", "design", "graphics", "social"},
	},
	{
		ID:          "plugin_miro",
		Slug:        "miro",
		DisplayName: "Miro Whiteboard",
		Category:    "Design & Creative",
		Description: "Collaborate on visual brainstorms, sticky notes, and mind maps.",
		Keywords:    []string{"miro", "whiteboard", "brainstorm", "diagrams"},
	},
	{
		ID:          "plugin_whimsical",
		Slug:        "whimsical",
		DisplayName: "Whimsical",
		Category:    "Design & Creative",
		Description: "Generate mind maps, user flowcharts, and wireframes instantly.",
		Keywords:    []string{"whimsical", "flowchart", "wireframe", "diagram"},
	},
	{
		ID:          "plugin_show_me_diagrams",
		Slug:        "show-me-diagrams",
		DisplayName: "Show Me Diagrams",
		Category:    "Design & Creative",
		Description: "Create and edit interactive diagrams, architecture charts, and flows.",
		Keywords:    []string{"diagram", "flowchart", "architecture", "mermaid"},
	},
	{
		ID:          "plugin_mermaid_charts",
		Slug:        "mermaid-charts",
		DisplayName: "Mermaid Chart",
		Category:    "Design & Creative",
		Description: "Render and edit text-based sequence, class, state, and ER diagrams.",
		Keywords:    []string{"mermaid", "sequence", "class", "uml"},
	},
	{
		ID:          "visualize@openai-bundled",
		Slug:        "visualize",
		DisplayName: "Interactive Visualizer",
		Category:    "Design & Creative",
		Description: "Render rich interactive 3D, physics, and mathematical visual widgets.",
		Keywords:    []string{"visualize", "interactive", "widget", "canvas"},
	},
	{
		ID:          "plugin_dalle_studio",
		Slug:        "dalle-studio",
		DisplayName: "DALL-E Creative Studio",
		Category:    "Design & Creative",
		Description: "Generate, inpaint, and stylize high-resolution images and artwork.",
		Keywords:    []string{"dalle", "image", "art", "creative", "generate"},
	},
	{
		ID:          "plugin_unsplash",
		Slug:        "unsplash",
		DisplayName: "Unsplash Photos",
		Category:    "Design & Creative",
		Description: "Search millions of free high-resolution stock photography.",
		Keywords:    []string{"unsplash", "photos", "stock", "images"},
	},
	{
		ID:          "plugin_speechify",
		Slug:        "speechify",
		DisplayName: "Speechify Voice",
		Category:    "Design & Creative",
		Description: "Natural voice text-to-speech generation and audio narration.",
		Keywords:    []string{"voice", "audio", "speech", "tts", "narrate"},
	},

	// --- Sales, Marketing & Finance (14) ---
	{
		ID:          "plugin_asdk_app_697d413990c88191a2bf4799604f8f6c",
		Slug:        "salesforce",
		DisplayName: "Salesforce CRM",
		Category:    "Sales & Finance",
		Description: "Query CRM records, contacts, accounts, leads, and sales pipelines.",
		Keywords:    []string{"crm", "salesforce", "leads", "sales", "pipeline"},
	},
	{
		ID:          "plugin_hubspot",
		Slug:        "hubspot",
		DisplayName: "HubSpot",
		Category:    "Sales & Finance",
		Description: "Inbound marketing, deal stages, email campaigns, and contact records.",
		Keywords:    []string{"hubspot", "marketing", "deals", "crm"},
	},
	{
		ID:          "plugin_apollo_io",
		Slug:        "apollo",
		DisplayName: "Apollo.io",
		Category:    "Sales & Finance",
		Description: "Lead generation, B2B contact intelligence, and sales email sequences.",
		Keywords:    []string{"leads", "b2b", "prospecting", "apollo"},
	},
	{
		ID:          "plugin_asdk_app_69c18c28f1188191bf5b8445c4ab0a2e",
		Slug:        "semrush",
		DisplayName: "Semrush",
		Category:    "Sales & Finance",
		Description: "SEO keyword analysis, backlink audits, competitor rankings, and search trends.",
		Keywords:    []string{"seo", "keywords", "semrush", "marketing", "rankings"},
	},
	{
		ID:          "plugin_stripe",
		Slug:        "stripe",
		DisplayName: "Stripe",
		Category:    "Sales & Finance",
		Description: "Query customer subscriptions, payment charges, invoices, and payouts.",
		Keywords:    []string{"stripe", "payments", "billing", "subscriptions", "money"},
	},
	{
		ID:          "plugin_shopify",
		Slug:        "shopify",
		DisplayName: "Shopify",
		Category:    "Sales & Finance",
		Description: "View e-commerce products, track orders, inventory levels, and store revenue.",
		Keywords:    []string{"shopify", "ecommerce", "store", "orders", "inventory"},
	},
	{
		ID:          "plugin_intercom",
		Slug:        "intercom",
		DisplayName: "Intercom",
		Category:    "Sales & Finance",
		Description: "Live customer support chat, ticketing, and AI help desk inbox.",
		Keywords:    []string{"intercom", "support", "helpdesk", "chat"},
	},
	{
		ID:          "plugin_zendesk",
		Slug:        "zendesk",
		DisplayName: "Zendesk",
		Category:    "Sales & Finance",
		Description: "Manage customer support tickets, satisfaction ratings, and knowledge bases.",
		Keywords:    []string{"zendesk", "support", "tickets", "help"},
	},
	{
		ID:          "plugin_quickbooks",
		Slug:        "quickbooks",
		DisplayName: "QuickBooks Online",
		Category:    "Sales & Finance",
		Description: "Bookkeeping, profit & loss reports, balance sheets, and tax expenses.",
		Keywords:    []string{"accounting", "quickbooks", "taxes", "invoices", "finance"},
	},
	{
		ID:          "plugin_xero",
		Slug:        "xero",
		DisplayName: "Xero",
		Category:    "Sales & Finance",
		Description: "Small business accounting, bank reconciliations, and financial reporting.",
		Keywords:    []string{"xero", "accounting", "finance", "ledger"},
	},
	{
		ID:          "plugin_plaid",
		Slug:        "plaid",
		DisplayName: "Plaid",
		Category:    "Sales & Finance",
		Description: "Connect bank accounts, verify identity, and track transaction balances.",
		Keywords:    []string{"plaid", "banking", "finance", "transactions"},
	},
	{
		ID:          "plugin_gong",
		Slug:        "gong",
		DisplayName: "Gong.io",
		Category:    "Sales & Finance",
		Description: "Revenue intelligence, sales call recordings, and objection analysis.",
		Keywords:    []string{"gong", "sales", "intelligence", "calls"},
	},
	{
		ID:          "plugin_close_crm",
		Slug:        "close-crm",
		DisplayName: "Close CRM",
		Category:    "Sales & Finance",
		Description: "High-velocity sales CRM, cold calling logs, and SMS follow-ups.",
		Keywords:    []string{"close", "crm", "sales", "calling"},
	},
	{
		ID:          "plugin_mailchimp",
		Slug:        "mailchimp",
		DisplayName: "Mailchimp",
		Category:    "Sales & Finance",
		Description: "Create email newsletters, manage audience lists, and check open rates.",
		Keywords:    []string{"mailchimp", "newsletter", "email", "campaign"},
	},
}

// buildRemotePluginDirectoryItem builds a valid RemotePluginDirectoryItem for both Rust CodexCLI and JS webview.
func buildRemotePluginDirectoryItem(pluginID string) map[string]any {
	slug := pluginID
	displayName := pluginID
	category := "Productivity"
	desc := displayName + " integration for ChatGPT"
	var keywords []string

	for _, def := range allPublicPluginsDef {
		if def.ID == pluginID || def.Slug == pluginID {
			slug = def.Slug
			displayName = def.DisplayName
			category = def.Category
			desc = def.Description
			keywords = def.Keywords
			break
		}
	}

	status := "AVAILABLE"
	installed := false
	enabled := false
	if isPluginInstalled(pluginID, slug) {
		status = "ENABLED"
		installed = true
		enabled = true
	}

	iconURL := getPluginIconDataURI(slug, displayName, category)

	return map[string]any{
		"id":                                  pluginID,
		"name":                                slug,
		"display_name":                        displayName,
		"short_description":                   desc,
		"scope":                               "GLOBAL",
		"discoverability":                     "LISTED",
		"status":                              status,
		"availability":                        status,
		"installed":                           installed,
		"enabled":                             enabled,
		"workspace_id":                        nil,
		"canonical_app_id":                    nil,
		"connector_id":                        nil,
		"creator_account_user_id":             nil,
		"creator_name":                        "OpenAI",
		"share_url":                           nil,
		"share_principals":                    []any{},
		"can_publish_to_workspace":            false,
		"installation_policy":                 "AVAILABLE",
		"installation_policy_source":          nil,
		"must_show_installation_interstitial": false,
		"authentication_policy":               "ON_USE",
		"disabled_reason":                     nil,
		"eligible_plan_types":                 []string{"free", "plus", "pro", "team", "enterprise"},
		"is_template":                         false,
		"requires_local_executor":             false,
		"icon_url":                            iconURL,
		"icon_url_dark":                       iconURL,
		"keywords":                            keywords,
		"release": map[string]any{
			"version":                 "1.0.0",
			"display_name":            displayName,
			"description":             desc,
			"bundle_download_url":     nil,
			"requires_local_executor": false,
			"app_ids":                 []string{},
			"app_manifest":            nil,
			"app_templates":           []any{},
			"keywords":                keywords,
			"skills":                  []any{},
			"onboarding_skill_name":   nil,
			"mcp_servers":             []any{},
			"scheduled_tasks":         []any{},
			"interface": map[string]any{
				"display_name":         displayName,
				"short_description":    desc,
				"long_description":     desc,
				"category":             category,
				"capabilities":         []string{},
				"brand_color":          nil,
				"developer_name":       "OpenAI",
				"website_url":          nil,
				"privacy_policy_url":   nil,
				"terms_of_service_url": nil,
				"default_prompt":       nil,
				"default_prompts":      []string{},
				"composer_icon_url":    iconURL,
				"logo_url":             iconURL,
				"logo_url_dark":        iconURL,
				"screenshot_urls":      []string{},
			},
		},
	}
}

func getAllPublicPlugins() []map[string]any {
	res := make([]map[string]any, 0, len(allPublicPluginsDef))
	for _, def := range allPublicPluginsDef {
		res = append(res, buildRemotePluginDirectoryItem(def.ID))
	}
	return res
}

func getFeaturedPluginIDs() []string {
	return []string{
		"plugin_connector_1p_1a69035c238881919c4190932b2df699",
		"plugin_connector_1p_f8509de903288191b14a160c6c5d20b0",
		"plugin_connector_1p_95d39881713c8191931482a62d6edff9",
		"plugin_asdk_app_69a1d78e929881919bba0dbda1f6436d",
		"plugin_connector_1p_ab21a553bfbc81919ea8fd1858e3ffa7",
		"documents@openai-primary-runtime",
		"pdf@openai-primary-runtime",
		"spreadsheets@openai-primary-runtime",
		"presentations@openai-primary-runtime",
		"wolfram@openai-primary-runtime",
		"plugin_figma",
		"plugin_asdk_app_697d413990c88191a2bf4799604f8f6c",
		"plugin_stripe",
		"deep-research@openai-bundled",
		"code-review@openai-bundled",
		"plugin_linear",
	}
}

var (
	installedPluginsMu  sync.RWMutex
	installedPluginsSet = map[string]bool{
		"documents@openai-primary-runtime":                        true,
		"pdf@openai-primary-runtime":                              true,
		"spreadsheets@openai-primary-runtime":                     true,
		"presentations@openai-primary-runtime":                    true,
		"template-creator@openai-primary-runtime":                 true,
		"plugin_connector_1p_f8509de903288191b14a160c6c5d20b0":   true,
		"plugin_asdk_app_69a1d78e929881919bba0dbda1f6436d":       true,
		"plugin_connector_1p_1a69035c238881919c4190932b2df699":   true,
		"plugin_linear":                                           true,
		"linear":                                                  true,
		"github":                                                  true,
		"documents":                                               true,
		"pdf":                                                     true,
		"spreadsheets":                                            true,
		"presentations":                                           true,
		"template-creator":                                        true,
	}
)

func isPluginInstalled(id, slug string) bool {
	installedPluginsMu.RLock()
	defer installedPluginsMu.RUnlock()
	return installedPluginsSet[id] || installedPluginsSet[slug]
}

func getInstalledPlugins() []map[string]any {
	installedPluginsMu.RLock()
	defer installedPluginsMu.RUnlock()

	var res []map[string]any
	seen := make(map[string]bool)
	for _, def := range allPublicPluginsDef {
		if installedPluginsSet[def.ID] || installedPluginsSet[def.Slug] {
			item := buildRemotePluginDirectoryItem(def.ID)
			item["installed"] = true
			item["status"] = "ENABLED"
			item["availability"] = "ENABLED"
			item["enabled"] = true
			res = append(res, item)
			seen[def.ID] = true
			seen[def.Slug] = true
		}
	}
	for id := range installedPluginsSet {
		if !seen[id] {
			item := buildRemotePluginDirectoryItem(id)
			item["installed"] = true
			item["status"] = "ENABLED"
			item["availability"] = "ENABLED"
			item["enabled"] = true
			res = append(res, item)
			seen[id] = true
		}
	}
	return res
}

// HandlePluginsHome serves GET /ps/plugins/home.
func (h *Handlers) HandlePluginsHome(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	allPlugins := getAllPublicPlugins()
	featuredIDs := getFeaturedPluginIDs()
	featuredMap := make(map[string]bool, len(featuredIDs))
	for _, fid := range featuredIDs {
		featuredMap[fid] = true
	}

	var featuredPlugins []map[string]any
	var devPlugins []map[string]any
	var prodPlugins []map[string]any
	var docPlugins []map[string]any
	var dataPlugins []map[string]any
	var designPlugins []map[string]any
	var salesPlugins []map[string]any

	for _, p := range allPlugins {
		id, _ := p["id"].(string)
		if featuredMap[id] {
			featuredPlugins = append(featuredPlugins, p)
		}
		if rel, ok := p["release"].(map[string]any); ok {
			if iface, ok := rel["interface"].(map[string]any); ok {
				cat, _ := iface["category"].(string)
				switch cat {
				case "Developer Tools":
					devPlugins = append(devPlugins, p)
				case "Productivity":
					prodPlugins = append(prodPlugins, p)
				case "Documents & Office":
					docPlugins = append(docPlugins, p)
				case "Data & Research":
					dataPlugins = append(dataPlugins, p)
				case "Design & Creative":
					designPlugins = append(designPlugins, p)
				case "Sales & Finance":
					salesPlugins = append(salesPlugins, p)
				default:
					prodPlugins = append(prodPlugins, p)
				}
			}
		}
	}

	sections := []map[string]any{
		{
			"id":       "featured",
			"title":    "Featured",
			"url_slug": "featured",
			"plugins":  featuredPlugins,
		},
		{
			"id":       "developer-tools",
			"title":    "Developer Tools & Engineering",
			"url_slug": "developer-tools",
			"plugins":  devPlugins,
		},
		{
			"id":       "productivity",
			"title":    "Productivity & Communication",
			"url_slug": "productivity",
			"plugins":  prodPlugins,
		},
		{
			"id":       "documents-office",
			"title":    "Documents, PDF & Office",
			"url_slug": "documents-office",
			"plugins":  docPlugins,
		},
		{
			"id":       "data-research",
			"title":    "Data Analytics, Science & Research",
			"url_slug": "data-research",
			"plugins":  dataPlugins,
		},
		{
			"id":       "design-creative",
			"title":    "Design, Visuals & Media",
			"url_slug": "design-creative",
			"plugins":  designPlugins,
		},
		{
			"id":       "business-sales",
			"title":    "Sales, Marketing & Finance",
			"url_slug": "business-sales",
			"plugins":  salesPlugins,
		},
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"sections": sections,
	})
}

// HandlePluginsInstalled serves GET/POST /ps/plugins/*.
func (h *Handlers) HandlePluginsInstalled(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimSuffix(r.URL.Path, "/")
	if strings.HasSuffix(path, "/home") {
		h.HandlePluginsHome(w, r)
		return
	}
	if strings.HasSuffix(path, "/featured") {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(getFeaturedPluginIDs())
		return
	}
	if strings.Contains(path, "/canonical-app/") {
		parts := strings.Split(path, "/")
		appID := parts[len(parts)-1]
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plugin_id": appID,
			"plugin":    buildRemotePluginDirectoryItem(appID),
		})
		return
	}
	if strings.HasSuffix(path, "/installed") {
		scope := r.URL.Query().Get("scope")
		all := getAllPublicPlugins()
		var res []map[string]any
		for _, p := range all {
			id, _ := p["id"].(string)
			slug, _ := p["name"].(string)
			installed := isPluginInstalled(id, slug)
			if installed {
				p["installed"] = true
				p["status"] = "ENABLED"
				p["availability"] = "ENABLED"
				p["enabled"] = true
			} else {
				p["installed"] = false
				p["status"] = "AVAILABLE"
				p["availability"] = "AVAILABLE"
				p["enabled"] = false
			}
			if scope == "USER" && !installed {
				continue
			}
			res = append(res, p)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plugins": res,
			"pagination": map[string]any{
				"next_page_token": nil,
			},
		})
		return
	}
	if strings.HasSuffix(path, "/list") {
		scope := r.URL.Query().Get("scope")
		plugins := getAllPublicPlugins()
		if scope == "USER" {
			plugins = getInstalledPlugins()
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plugins": plugins,
			"pagination": map[string]any{
				"next_page_token": nil,
			},
		})
		return
	}
	if strings.HasSuffix(path, "/search") {
		q := strings.ToLower(r.URL.Query().Get("q"))
		allPlugins := getAllPublicPlugins()
		var matched []map[string]any
		for _, p := range allPlugins {
			name, _ := p["name"].(string)
			displayName, _ := p["display_name"].(string)
			desc, _ := p["short_description"].(string)
			if q == "" || strings.Contains(strings.ToLower(name), q) ||
				strings.Contains(strings.ToLower(displayName), q) ||
				strings.Contains(strings.ToLower(desc), q) {
				matched = append(matched, p)
			}
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plugins": matched,
			"pagination": map[string]any{
				"next_page_token": nil,
			},
		})
		return
	}
	if strings.Contains(path, "/suggested") || strings.HasSuffix(path, "/created") || strings.HasSuffix(path, "/shared") || strings.HasSuffix(path, "/codex") {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plugins": getAllPublicPlugins(),
			"pagination": map[string]any{
				"next_page_token": nil,
			},
		})
		return
	}
	if strings.HasSuffix(path, "/template-instances") {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"instances": []any{},
			"pagination": map[string]any{
				"next_page_token": nil,
			},
		})
		return
	}
	if strings.HasSuffix(path, "/batch") {
		var reqBody struct {
			PluginIDs []string `json:"plugin_ids"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
		}
		results := make([]any, 0, len(reqBody.PluginIDs))
		for _, pid := range reqBody.PluginIDs {
			results = append(results, map[string]any{
				"requested_plugin_id": pid,
				"plugin":              buildRemotePluginDirectoryItem(pid),
			})
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": results,
		})
		return
	}
	if strings.HasSuffix(path, "/install") || strings.HasSuffix(path, "/uninstall") ||
		strings.HasSuffix(path, "/enable") || strings.HasSuffix(path, "/disable") {
		parts := strings.Split(path, "/")
		var targetPluginID string
		if len(parts) >= 2 && parts[len(parts)-2] != "plugins" {
			targetPluginID = parts[len(parts)-2]
		}
		if targetPluginID == "" && r.Body != nil {
			var body struct {
				PluginID string `json:"plugin_id"`
				ID       string `json:"id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.PluginID != "" {
				targetPluginID = body.PluginID
			} else if body.ID != "" {
				targetPluginID = body.ID
			}
		}

		if targetPluginID != "" {
			installedPluginsMu.Lock()
			if strings.HasSuffix(path, "/install") || strings.HasSuffix(path, "/enable") {
				installedPluginsSet[targetPluginID] = true
			} else {
				delete(installedPluginsSet, targetPluginID)
			}
			installedPluginsMu.Unlock()
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
		})
		return
	}
	if strings.Contains(path, "/skills/") {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
		})
		return
	}
	parts := strings.Split(path, "/")
	pluginID := parts[len(parts)-1]
	if pluginID != "" && pluginID != "plugins" {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(buildRemotePluginDirectoryItem(pluginID))
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"plugins": getAllPublicPlugins(),
		"pagination": map[string]any{
			"next_page_token": nil,
		},
	})
}

// HandlePluginsFeatured serves GET /plugins/featured.
func (h *Handlers) HandlePluginsFeatured(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(getFeaturedPluginIDs())
}

// HandlePluginCategories serves GET /ps/plugin-categories/{category_slug}/plugins.
func (h *Handlers) HandlePluginCategories(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	categorySlug := "featured"
	for i, part := range parts {
		if part == "plugin-categories" && i+1 < len(parts) {
			categorySlug = parts[i+1]
			break
		}
	}

	allPlugins := getAllPublicPlugins()
	var filtered []map[string]any
	if categorySlug == "featured" {
		featuredIDs := getFeaturedPluginIDs()
		fMap := make(map[string]bool, len(featuredIDs))
		for _, fid := range featuredIDs {
			fMap[fid] = true
		}
		for _, p := range allPlugins {
			if id, _ := p["id"].(string); fMap[id] {
				filtered = append(filtered, p)
			}
		}
	} else {
		catMap := map[string]string{
			"developer-tools":  "Developer Tools",
			"productivity":     "Productivity",
			"documents-office": "Documents & Office",
			"data-research":    "Data & Research",
			"design-creative":  "Design & Creative",
			"business-sales":   "Sales & Finance",
		}
		targetCat := catMap[categorySlug]
		for _, p := range allPlugins {
			if rel, ok := p["release"].(map[string]any); ok {
				if iface, ok := rel["interface"].(map[string]any); ok {
					cat, _ := iface["category"].(string)
					if targetCat != "" && cat == targetCat {
						filtered = append(filtered, p)
					} else if targetCat == "" {
						cleanSlug := strings.ReplaceAll(categorySlug, "-", " ")
						if strings.Contains(strings.ToLower(cat), strings.ToLower(cleanSlug)) ||
							strings.Contains(strings.ToLower(cleanSlug), strings.ToLower(cat)) {
							filtered = append(filtered, p)
						}
					}
				}
			}
		}
	}

	if len(filtered) == 0 {
		filtered = allPlugins
	}

	// Filter by search query if present
	if q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))); q != "" {
		var matched []map[string]any
		for _, p := range filtered {
			name, _ := p["name"].(string)
			displayName, _ := p["display_name"].(string)
			desc, _ := p["short_description"].(string)
			if strings.Contains(strings.ToLower(name), q) ||
				strings.Contains(strings.ToLower(displayName), q) ||
				strings.Contains(strings.ToLower(desc), q) {
				matched = append(matched, p)
			}
		}
		filtered = matched
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"plugins": filtered,
		"pagination": map[string]any{
			"next_page_token": nil,
		},
	})
}

// HandleConnectorsDirectoryList serves GET /connectors/directory/list* so app/list succeeds.
func (h *Handlers) HandleConnectorsDirectoryList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	allPlugins := getAllPublicPlugins()
	apps := make([]map[string]any, 0, len(allPlugins))
	for _, p := range allPlugins {
		id, _ := p["id"].(string)
		name, _ := p["name"].(string)
		displayName, _ := p["display_name"].(string)
		desc, _ := p["short_description"].(string)
		apps = append(apps, map[string]any{
			"id":           id,
			"name":         name,
			"display_name": displayName,
			"description":  desc,
			"logo_url":     nil,
			"auth_type":    "oauth",
			"is_installed": true,
			"can_install":  true,
		})
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"apps":        apps,
		"next_cursor": nil,
		"nextCursor":  nil,
	})
}

// HandlePsApps serves GET/POST /ps/apps/*.
func (h *Handlers) HandlePsApps(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var reqBody struct {
		AppIDs []string `json:"app_ids"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
	}
	apps := make([]map[string]any, 0, len(reqBody.AppIDs))
	for _, id := range reqBody.AppIDs {
		apps = append(apps, map[string]any{
			"id":                   id,
			"name":                 id,
			"display_name":         id,
			"description":          "",
			"icon_url":             nil,
			"icon_dark_url":        nil,
			"tools":                []any{},
			"distribution_channel": nil,
		})
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"apps": apps,
	})
}

// HandleAipConnectors serves GET/POST /aip/connectors/*.
func (h *Handlers) HandleAipConnectors(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimSuffix(r.URL.Path, "/")
	if strings.Contains(path, "/links") {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"links": []any{},
		})
		return
	}
	if strings.HasSuffix(path, "/link") {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"link": nil,
		})
		return
	}
	if strings.HasSuffix(path, "/has_installations") {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"has_installations": false,
		})
		return
	}
	if strings.HasSuffix(path, "/batch") {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"connectors": []any{},
		})
		return
	}
	parts := strings.Split(path, "/")
	connID := parts[len(parts)-1]
	if connID == "" || connID == "connectors" {
		connID = "connector_local"
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":          connID,
		"name":        "Connector",
		"description": "",
		"status":      "ENABLED",
		"actions":     []any{},
		"supported_auth": []any{
			map[string]any{"type": "NONE"},
		},
	})
}


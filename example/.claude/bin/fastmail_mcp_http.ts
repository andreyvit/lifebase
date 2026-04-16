#!/usr/bin/env -S deno run --allow-read --allow-net

import { McpServer } from "npm:@modelcontextprotocol/sdk@1.29.0/server/mcp.js";
import { WebStandardStreamableHTTPServerTransport } from "npm:@modelcontextprotocol/sdk@1.29.0/server/webStandardStreamableHttp.js";
import JamClient from "npm:jmap-jam@0.13.1";

const HTTP_HOST = "127.0.0.1";
const HTTP_PORT = 43124;
const HTTP_PATH = "/mcp";
const SETTINGS_LOCAL_JSON = new URL("../settings.local.json", import.meta.url);

type Config = {
  sessionUrl: string;
  bearerToken: string;
  accountId?: string;
};

type Context = {
  jam: JamClient;
  accountId: string;
  isReadOnly: boolean;
};

type ToolRegistrars = {
  registerEmailTools: (...args: unknown[]) => void;
  registerEmailSubmissionTools: (...args: unknown[]) => void;
};

function fail(message: string): never {
  console.error(message);
  Deno.exit(1);
}

async function loadConfig(): Promise<Config> {
  let raw: string;
  try {
    raw = await Deno.readTextFile(SETTINGS_LOCAL_JSON);
  } catch (error) {
    fail(`Failed to read ${SETTINGS_LOCAL_JSON.pathname}: ${error}`);
  }

  let parsed: Record<string, unknown>;
  try {
    parsed = JSON.parse(raw) as Record<string, unknown>;
  } catch (error) {
    fail(`Failed to parse ${SETTINGS_LOCAL_JSON.pathname}: ${error}`);
  }

  const env = parsed.env;
  if (!env || typeof env !== "object") {
    fail(`${SETTINGS_LOCAL_JSON.pathname} has no env object`);
  }

  const localEnv = env as Record<string, unknown>;
  const sessionUrl = localEnv.JMAP_SESSION_URL;
  const bearerToken = localEnv.JMAP_BEARER_TOKEN;
  const accountId = localEnv.JMAP_ACCOUNT_ID;

  if (typeof sessionUrl !== "string" || !sessionUrl) {
    fail(`${SETTINGS_LOCAL_JSON.pathname} is missing JMAP_SESSION_URL`);
  }
  if (typeof bearerToken !== "string" || !bearerToken) {
    fail(`${SETTINGS_LOCAL_JSON.pathname} is missing JMAP_BEARER_TOKEN`);
  }
  if (accountId !== undefined && typeof accountId !== "string") {
    fail(`${SETTINGS_LOCAL_JSON.pathname} has a non-string JMAP_ACCOUNT_ID`);
  }

  return { sessionUrl, bearerToken, accountId };
}

async function initializeContext(): Promise<Context> {
  const config = await loadConfig();
  const jam = new JamClient({
    sessionUrl: config.sessionUrl,
    bearerToken: config.bearerToken,
  });

  const accountId = config.accountId || await jam.getPrimaryAccount();
  const session = await jam.session;
  const account = session.accounts[accountId];
  if (!account) {
    fail(`JMAP account ${accountId} was not found in the Fastmail session`);
  }

  console.error(`Fastmail JMAP ready for account ${accountId}`);
  return { jam, accountId, isReadOnly: account.isReadOnly };
}

async function loadToolRegistrars(): Promise<ToolRegistrars> {
  const emailModuleUrl = "https://jsr.io/@wyattjoh/jmap-mcp/0.6.1/src/tools/email.ts";
  const submissionModuleUrl =
    "https://jsr.io/@wyattjoh/jmap-mcp/0.6.1/src/tools/submission.ts";
  const emailModule = await import(emailModuleUrl);
  const submissionModule = await import(submissionModuleUrl);

  const registerEmailTools = emailModule.registerEmailTools;
  const registerEmailSubmissionTools = submissionModule.registerEmailSubmissionTools;
  if (typeof registerEmailTools !== "function") {
    fail("Failed to load registerEmailTools from jmap-mcp");
  }
  if (typeof registerEmailSubmissionTools !== "function") {
    fail("Failed to load registerEmailSubmissionTools from jmap-mcp");
  }

  return {
    registerEmailTools,
    registerEmailSubmissionTools,
  };
}

function createServer(context: Context, tools: ToolRegistrars): McpServer {
  const { registerEmailTools, registerEmailSubmissionTools } = tools;
  const server = new McpServer({
    name: "fastmail",
    version: "0.6.1-http",
  });

  registerEmailTools(server, context.jam, context.accountId, context.isReadOnly);
  console.error("Registered JMAP mail tools");

  if (!context.isReadOnly) {
    registerEmailSubmissionTools(server, context.jam, context.accountId);
    console.error("Registered JMAP submission tools");
  } else {
    console.error("Fastmail account is read-only; submission tools disabled");
  }

  return server;
}

function json(body: Record<string, unknown>, init?: ResponseInit): Response {
  return new Response(JSON.stringify(body), {
    ...init,
    headers: {
      "content-type": "application/json",
      ...(init?.headers ?? {}),
    },
  });
}

async function main(): Promise<void> {
  const context = await initializeContext();
  const tools = await loadToolRegistrars();
  const transports = new Map<string, WebStandardStreamableHTTPServerTransport>();

  console.error(`Starting Fastmail MCP HTTP server on http://${HTTP_HOST}:${HTTP_PORT}${HTTP_PATH}`);

  Deno.serve({ hostname: HTTP_HOST, port: HTTP_PORT }, async (request) => {
    const url = new URL(request.url);

    if (url.pathname === "/health") {
      return json({
        status: "ok",
        activeSessions: transports.size,
      });
    }

    if (url.pathname !== HTTP_PATH) {
      return new Response("Not found", { status: 404 });
    }

    const existingSessionId = request.headers.get("mcp-session-id");
    let transport = existingSessionId ? transports.get(existingSessionId) : undefined;

    if (!transport) {
      if (existingSessionId) {
        return json({
          jsonrpc: "2.0",
          error: {
            code: -32000,
            message: "Unknown MCP session",
          },
          id: null,
        }, { status: 404 });
      }

      transport = new WebStandardStreamableHTTPServerTransport({
        sessionIdGenerator: () => crypto.randomUUID(),
        onsessioninitialized: (sessionId: string) => {
          transports.set(sessionId, transport!);
          console.error(`Fastmail MCP session initialized: ${sessionId}`);
        },
      });
      transport.onclose = () => {
        if (transport?.sessionId) {
          console.error(`Fastmail MCP session closed: ${transport.sessionId}`);
          transports.delete(transport.sessionId);
        }
      };

      const server = createServer(context, tools);
      await server.connect(transport);
    }

    try {
      return await transport.handleRequest(request);
    } catch (error) {
      console.error(`Error handling Fastmail MCP request: ${error}`);
      return json({
        jsonrpc: "2.0",
        error: {
          code: -32603,
          message: "Internal server error",
        },
        id: null,
      }, { status: 500 });
    }
  });
}

if (import.meta.main) {
  main().catch((error) => {
    console.error(`Fatal Fastmail MCP wrapper error: ${error}`);
    Deno.exit(1);
  });
}

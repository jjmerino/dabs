// dabash (pi bridge) — connects pi to a dabs box's MCP server and exposes
// EVERY tool it advertises as a pi tool. There is ONE implementation of the
// tools (in `dabs mcp`); this extension is a thin MCP client, so as the MCP
// server grows, pi gets the new tools with no changes here.
//
// The agent runs on the host with only these tools; each call is executed
// inside the box named by DABS_NAME. If DABS_NAME is unset (no box assigned),
// the bridge registers nothing — the agent gets no shell on the host.
//
// Install with `dabs install pi`; remove with `dabs uninstall pi`.

import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { createInterface } from "node:readline";
import { Type } from "@sinclair/typebox";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

interface McpTool {
	name: string;
	description?: string;
	inputSchema?: { properties?: Record<string, { type?: string; description?: string }>; required?: string[] };
}

export default async function dabashBridge(pi: ExtensionAPI) {
	const box = process.env.DABS_NAME;
	if (!box) {
		// No box assigned ⇒ expose no tools. The agent cannot touch the host.
		return;
	}

	const client = new McpStdioClient(["mcp", box]);
	await client.initialize();
	const tools = await client.listTools();

	for (const tool of tools) {
		pi.registerTool({
			name: tool.name,
			label: tool.name,
			description: tool.description ?? tool.name,
			parameters: toTypeBox(tool.inputSchema),
			async execute(_id, params) {
				const result = await client.callTool(tool.name, params as Record<string, unknown>);
				return result;
			},
		});
	}

	pi.on("session_shutdown" as never, () => client.close());
}

// toTypeBox builds a permissive TypeBox object schema from an MCP JSON
// inputSchema — enough for the model to see the parameters and for pi to
// validate. Unknown property types fall back to a permissive Any.
function toTypeBox(schema: McpTool["inputSchema"]) {
	const props = schema?.properties ?? {};
	const required = new Set(schema?.required ?? []);
	const fields: Record<string, ReturnType<typeof Type.Any>> = {};
	for (const [key, spec] of Object.entries(props)) {
		let t;
		switch (spec.type) {
			case "string":
				t = Type.String({ description: spec.description });
				break;
			case "number":
			case "integer":
				t = Type.Number({ description: spec.description });
				break;
			case "boolean":
				t = Type.Boolean({ description: spec.description });
				break;
			default:
				t = Type.Any({ description: spec.description });
		}
		fields[key] = required.has(key) ? t : Type.Optional(t);
	}
	return Type.Object(fields);
}

// McpStdioClient speaks newline-delimited JSON-RPC 2.0 to a `dabs mcp <box>`
// subprocess over stdio, correlating responses by id.
class McpStdioClient {
	private proc: ChildProcessWithoutNullStreams;
	private nextId = 1;
	private pending = new Map<number, (msg: any) => void>();

	constructor(args: string[]) {
		this.proc = spawn("dabs", args, { stdio: ["pipe", "pipe", "inherit"] });
		const rl = createInterface({ input: this.proc.stdout });
		rl.on("line", (line) => {
			if (!line.trim()) return;
			let msg: any;
			try {
				msg = JSON.parse(line);
			} catch {
				return;
			}
			const resolve = this.pending.get(msg.id);
			if (resolve) {
				this.pending.delete(msg.id);
				resolve(msg);
			}
		});
	}

	private request(method: string, params?: unknown): Promise<any> {
		const id = this.nextId++;
		return new Promise((resolve) => {
			this.pending.set(id, resolve);
			this.proc.stdin.write(`${JSON.stringify({ jsonrpc: "2.0", id, method, params })}\n`);
		});
	}

	async initialize(): Promise<void> {
		await this.request("initialize", { protocolVersion: "2024-11-05", capabilities: {} });
	}

	async listTools(): Promise<McpTool[]> {
		const res = await this.request("tools/list");
		return res.result?.tools ?? [];
	}

	async callTool(name: string, args: Record<string, unknown>) {
		const res = await this.request("tools/call", { name, arguments: args });
		const r = res.result ?? {};
		return { isError: Boolean(r.isError), content: r.content ?? [{ type: "text", text: "" }] };
	}

	close(): void {
		this.proc.stdin.end();
		this.proc.kill();
	}
}

// dabash — a pi extension exposing ONE tool: a shell that only runs inside a
// dabs box. Meant for a harness launched INSIDE a box; the tool refuses on a
// real host (no DABS_NAME), so it never executes a command off-box.
//
// Install with `dabs install pi`; remove with `dabs uninstall pi`.

import { execFile } from "node:child_process";
import { Type } from "@sinclair/typebox";
import { defineTool } from "@earendil-works/coding-agent";

const dabash = defineTool({
	name: "dabash",
	description:
		"Run a shell command inside your machine. This is your only capability; " +
		"there is no other filesystem or host.",
	parameters: Type.Object({
		command: Type.String({ description: "shell command line" }),
		cwd: Type.Optional(Type.String({ description: "directory to run in" })),
	}),
	async execute({ command, cwd }) {
		// The guard: DABS_NAME is set by dabs on every box it creates. Absent
		// ⇒ we are NOT inside a sandbox ⇒ refuse rather than run a real shell.
		if (!process.env.DABS_NAME) {
			return {
				isError: true,
				content:
					"dabash refused: not inside a dabs box (no DABS_NAME). This tool " +
					"only runs commands inside a sandbox; it will not touch a real host.",
			};
		}
		const line = cwd ? `cd ${shellQuote(cwd)} && (${command})` : command;
		return await new Promise((resolve) => {
			execFile("sh", ["-c", line], { maxBuffer: 32 * 1024 * 1024 }, (err, stdout, stderr) => {
				const out = (stdout || "") + (stderr || "");
				resolve(err ? { isError: true, content: `${out}\n${err.message}` } : { content: out });
			});
		});
	},
});

function shellQuote(s: string): string {
	return "'" + s.replaceAll("'", "'\\''") + "'";
}

// pi loads the default export as the extension's tool set.
export default { tools: [dabash] };

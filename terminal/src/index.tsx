#!/usr/bin/env node
import React from "react";
import { render } from "ink";
import { App } from "./app.js";
import { readEnvironment } from "./api.js";

try {
  render(<App environment={readEnvironment()} />);
} catch (error) {
  process.stderr.write(`assign-discuss: ${error instanceof Error ? error.message : "startup failed"}\n`);
  process.exitCode = 1;
}

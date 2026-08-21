import { render } from "preact";
import { App } from "./App";
import { boot } from "./state";
import "./styles.css";

const root = document.getElementById("app");
if (!root) throw new Error("missing #app element");

render(<App />, root);
void boot();

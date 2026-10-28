import {createRoot} from "react-dom/client";
import App from "./App";
import "./style.css";

// No StrictMode: its double mount would open every terminal session twice.
createRoot(document.getElementById("root")!).render(<App/>);

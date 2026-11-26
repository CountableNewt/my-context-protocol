import type { Metadata } from "next";
import Link from "next/link";
import { ArrowRight, ArrowUpRight } from "lucide-react";
import { MAIN_CONTENT_ID } from "@/lib/a11y";
import { glassSurfaceClasses } from "@/lib/glass";
import styles from "./landing.module.css";

const sourceUrl = "https://github.com/CountableNewt/my-context-protocol";

export const metadata: Metadata = {
  title: "MyContextProtocol — your context, connected",
  description:
    "Keep your conventions and workflows in Git as SKILL.md files. MyContextProtocol makes them available to your AI tools through a hosted MCP endpoint.",
};

/** Public by design: this page must render without a session or backend data. */
export default function LandingPage() {
  return (
    <div className={styles.landing}>
      <div className={styles.container}>
        <header className={styles.header}>
          <Link href="/" className={styles.wordmark}>
            MyContextProtocol
          </Link>
          <nav aria-label="Main navigation" className={styles.navigation}>
            <a href={sourceUrl}>GitHub</a>
            <Link href="/login" className={glassSurfaceClasses("default", styles.signIn)}>
              Sign in
            </Link>
          </nav>
        </header>

        <main id={MAIN_CONTENT_ID} tabIndex={-1}>
          <section className={styles.hero} aria-labelledby="landing-title">
            <div className={styles.introduction}>
              <h1 id="landing-title">
                You’ve explained<br /><span>this before.</span>
              </h1>
              <p>
                Your conventions. Your workflows. That one thing your AI keeps
                forgetting. Keep them in Git as <code>SKILL.md</code> files, and
                share them with your AI tools through a hosted MCP endpoint.
              </p>
              <Link href="/login" className={styles.getStarted}>
                Get started <ArrowRight size={18} aria-hidden="true" />
              </Link>
            </div>

            <div className={styles.preview}>
              <figure
                aria-label="Example SKILL.md file"
                className={glassSurfaceClasses("elevated", styles.skill)}
              >
                <figcaption>code-review / SKILL.md</figcaption>
                <pre>
                  <code>{"# Before you ship\n\n- Question the assumptions.\n- Look for the edge cases.\n- Leave it better than you found it."}</code>
                </pre>
                <p>Your instructions. Ready for your agent.</p>
              </figure>
            </div>
          </section>

          <section className={styles.workflow} aria-labelledby="workflow-title">
            <h2 id="workflow-title">How it works</h2>
            <div>
              <p className={styles.workflowHeadline}>Keep it in Git. Connect it with MCP.</p>
              <p className={styles.workflowDescription}>
                Connect a repository, publish your skills, and add your project’s
                endpoint to a compatible AI tool. Review releases and requests
                from your dashboard.
              </p>
            </div>
          </section>
        </main>

        <footer className={styles.footer}>
          <p>Open source. Full feature access.</p>
          <a href={sourceUrl}>
            Explore the source <ArrowUpRight size={16} aria-hidden="true" />
          </a>
        </footer>
      </div>
    </div>
  );
}

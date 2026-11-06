export default function App() {
  return (
    <main className="workspace">
      <header className="brand">
        <span className="brand-mark" aria-hidden="true">
          P
        </span>
        <span>Penguin</span>
      </header>
      <section className="welcome" aria-labelledby="welcome-title">
        <p className="eyebrow">Your voice. Your choice.</p>
        <h1 id="welcome-title">Switch providers, not your workflow.</h1>
        <p className="description">
          A workspace for speech with local models, your own API keys, and
          self-hosted servers.
        </p>
        <p className="status">
          The desktop foundation is ready. Speech generation is coming next.
        </p>
      </section>
      <footer>Local first. Open source.</footer>
    </main>
  );
}

export default function Page() {
  return <main className="shell"><aside><b>Papi</b><a>Overview</a><a>Accounts</a><a>Models</a><a>API Keys</a><a>Settings</a><a>Audit</a></aside><section><p className="eyebrow">Phase A control plane</p><h1>Dashboard backend foundation</h1><div className="grid">{['PostgreSQL source of truth','Redis version notifications','Envelope-encrypted credentials','Versioned gateway snapshots'].map(x => <article className="card" key={x}><h2>{x}</h2><p>Available through /api/control/v1.</p></article>)}</div></section></main>;
}

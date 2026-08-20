export type View = 'overview' | 'requests' | 'accounts' | 'models' | 'keys' | 'teams' | 'audit' | 'settings';

const VIEW_PATHS: Record<View, string> = {
  overview: '/',
  requests: '/requests',
  accounts: '/accounts',
  models: '/models',
  keys: '/keys',
  teams: '/teams',
  audit: '/audit',
  settings: '/settings',
};

const ALL_VIEWS = Object.keys(VIEW_PATHS) as View[];

export function viewFromPath(pathname: string): View {
  const normalized = pathname.split('?')[0].split('#')[0].replace(/\/+$/, '') || '/';
  for (const view of ALL_VIEWS) {
    if (VIEW_PATHS[view] === normalized) return view;
  }
  return 'overview';
}

export function pathForView(view: View): string {
  return VIEW_PATHS[view];
}

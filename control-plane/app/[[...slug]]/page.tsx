import DashboardClient from '../dashboard-client';
import { getRequestLocale } from '../request-locale';

// Catch-all page: the dashboard is a single-page app whose views are routed
// via the History API (see dashboard-client.tsx). Serving the same client for
// every path makes deep links like /models or /keys work on hard reload.
export default async function Page() {
  return <DashboardClient initialLocale={await getRequestLocale()} />;
}

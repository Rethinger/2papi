import DashboardClient from './dashboard-client';
import { getRequestLocale } from './request-locale';

export default async function Page() {
  return <DashboardClient initialLocale={await getRequestLocale()} />;
}

import './styles.css';
import { getRequestLocale } from './request-locale';

export const metadata = {
  title: '2papi Control Plane',
  description: 'Local control plane for multi-account AI routing',
};

export default async function RootLayout({ children }: { children: React.ReactNode }) {
  return <html lang={await getRequestLocale()}><body>{children}</body></html>;
}

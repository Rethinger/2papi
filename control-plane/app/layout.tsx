import './styles.css';

export const metadata = {
  title: '2papi Control Plane',
  description: 'Local control plane for multi-account AI routing',
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return <html lang="en"><body>{children}</body></html>;
}

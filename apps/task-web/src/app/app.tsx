// 应用装配：连接 → 路由 → 页面。hash 路由避免静态回退歧义。

import {QueryClientProvider} from '@tanstack/react-query';
import {useMemo} from 'react';
import {HashRouter, Route, Routes} from 'react-router-dom';
import {createQueryClientWorker} from '../lib/queries/client';
import {useTheme} from '../lib/theme/useTheme';
import {ConnectionProvider, useConnection} from '../features/connection/connection';
import {ShellLayout} from './layout';
import {ConnectPage} from '../features/connection/connect-page';
import {TaskListPage} from '../features/tasks/task-list-page';
import {TaskNewPage} from '../features/tasks/task-new-page';
import {TaskDetailLayout} from '../features/tasks/detail/task-detail-layout';
import {SettingsPage} from '../features/settings/settings-page';

function DidConnectGate({children}: {children: React.ReactNode}) {
  const {connected} = useConnection();
  return connected ? <>{children}</> : <ConnectPage />;
}

export function App() {
  useTheme();
  const queryClient = useMemo(() => createQueryClientWorker(), []);
  return (
    <QueryClientProvider client={queryClient}>
      <ConnectionProvider>
        <HashRouter>
          <DidConnectGate>
            <ShellLayout>
              <Routes>
                <Route index element={<TaskListPage />} />
                <Route path="tasks/new" element={<TaskNewPage />} />
                <Route path="tasks/:taskId/*" element={<TaskDetailLayout />} />
                <Route path="settings" element={<SettingsPage />} />
                <Route path="*" element={<TaskListPage />} />
              </Routes>
            </ShellLayout>
          </DidConnectGate>
        </HashRouter>
      </ConnectionProvider>
    </QueryClientProvider>
  );
}

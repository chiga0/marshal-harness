// 应用装配：连接 → 路由 → 页面。hash 路由避免静态回退歧义。

import {QueryClientProvider} from '@tanstack/react-query';
import {useEffect, useMemo} from 'react';
import {HashRouter, Route, Routes} from 'react-router-dom';
import {createQueryClientWorker} from '../lib/queries/client';
import {useTheme} from '../lib/theme/useTheme';
import {installBeforeUnloadGuard, LogicalActionScope} from '../features/tasks/detail/shared/logical-action';
import {ConnectionProvider, useConnection} from '../features/connection/connection';
import {ShellLayout} from './layout';
import {ConnectPage} from '../features/connection/connect-page';
import {TaskListPage} from '../features/tasks/task-list-page';
import {TaskNewPage} from '../features/tasks/task-new-page';
import {TaskDetailLayout} from '../features/tasks/detail/task-detail-layout';
import {SettingsPage} from '../features/settings/settings-page';

function DidConnectGate({children}: {children: React.ReactNode}) {
  const {connected, transport} = useConnection();
  return connected ? <LogicalActionScope session={transport}>{children}</LogicalActionScope> : <ConnectPage />;
}

export function App() {
  useTheme();
  useEffect(() => installBeforeUnloadGuard(), []); // ADR0098 §8：未决写操作时离开/刷新前提示（尽力而为）
  const queryClient = useMemo(() => createQueryClientWorker(), []);
  return (
    <QueryClientProvider client={queryClient}>
      <ConnectionProvider>
        <HashRouter>
          <DidConnectGate>
            <AppRoutes />
          </DidConnectGate>
        </HashRouter>
      </ConnectionProvider>
    </QueryClientProvider>
  );
}

/** 设置替换工作台外壳，不与主导航叠放。 */
export function AppRoutes() {
  return <Routes>
    <Route path="settings/*" element={<SettingsPage />} />
    <Route path="*" element={<ShellLayout><Routes>
      <Route index element={<TaskListPage />} />
      <Route path="tasks/new" element={<TaskNewPage />} />
      <Route path="tasks/:taskId/*" element={<TaskDetailLayout />} />
      <Route path="*" element={<TaskListPage />} />
    </Routes></ShellLayout>} />
  </Routes>;
}

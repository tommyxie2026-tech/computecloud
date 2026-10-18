#!/usr/bin/env python3
"""隔离 Server/fixture Worker 与 Chromium 的 Web 登录和任务验收。"""
import sys
from ci_control_client_e2e import main
if __name__ == '__main__':
    if '--output' not in sys.argv:
        sys.argv += ['--output', 'dist/control-web-auth/report.json']
    sys.argv.append('--web-auth')
    main()

# Third-party notices

## KLineChart

- Project: <https://github.com/klinecharts/KLineChart>
- Version: `10.0.2`
- License: Apache License 2.0
- Bundled file: `internal/localclient/ui/klinecharts.min.js`
- SHA-256: `db288a8c5d910a907f1e74fd355bc1d7a9219022549e7dc575e3076e5c31b46a`

The upstream license text is bundled at `internal/localclient/ui/klinecharts.LICENSE.txt` and is also included in the go-client binary through `go:embed`.

## Formula-TS

- Project: `DTrader-store/formula-ts`
- Source revision: `c149cb603ad0df7ea1acb259e6be6af06263bc6f`
- Source: <https://github.com/DTrader-store/formula-ts>
- License: MIT
- Vendored source and license: `web/vendor/formula-ts/`

Market Bridge carries compatibility patches for dynamic-period reference functions, TongDaXin EMA seeding, and explicitly warned future functions (`BACKSET`, `ZIG`, `PEAK`/`TROUGH`). The browser bundle is generated into `internal/localclient/ui/formula-worker.js`.

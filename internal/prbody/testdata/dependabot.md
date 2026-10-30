Bumps [zizmorcore/zizmor-action](https://github.com/zizmorcore/zizmor-action) from 0.6.3 to 0.8.0.
<details>
<summary>Release notes</summary>
<p><em>Sourced from <a href="https://github.com/zizmorcore/zizmor-action/releases">zizmorcore/zizmor-action's releases</a>.</em></p>
<blockquote>
<h2>v0.8.0</h2>
<p>The action now supports prereleases.</p>
<p>The default has not changed, but you can pass a prerelease version
of zizmor or <code>version: prerelease</code> to get whatever latest
prerelease the action knows about.</p>
<h2>What's Changed</h2>
<ul>
<li>Support prereleases by <a href="https://github.com/woodruffw"><code>@​woodruffw</code></a> in <a href="https://redirect.github.com/zizmorcore/zizmor-action/pull/182">zizmorcore/zizmor-action#182</a></li>
<li>Sync zizmor versions by <a href="https://github.com/github-actions"><code>@​github-actions</code></a>[bot] in <a href="https://redirect.github.com/zizmorcore/zizmor-action/pull/184">zizmorcore/zizmor-action#184</a></li>
</ul>
<p><strong>Full Changelog</strong>: <a href="https://github.com/zizmorcore/zizmor-action/compare/v0.7.1...v0.8.0">https://github.com/zizmorcore/zizmor-action/compare/v0.7.1...v0.8.0</a></p>
<h2>v0.7.1</h2>
<h1>zizmor changes</h1>
<h2>Bug Fixes 🐛🔗</h2>
<ul>
<li>Fixed a regression in 1.31.0 where some ignore comments/configurations would no longer apply to the dangerous-triggers audit</li>
</ul>
<h1>action changes</h1>
<p>The default version of zizmor is now 1.31.1.</p>
<h2>What's Changed</h2>
<ul>
<li>Sync zizmor versions by <a href="https://github.com/github-actions"><code>@​github-actions</code></a>[bot] in <a href="https://redirect.github.com/zizmorcore/zizmor-action/pull/178">zizmorcore/zizmor-action#178</a></li>
</ul>
<p><strong>Full Changelog</strong>: <a href="https://github.com/zizmorcore/zizmor-action/compare/v0.7.0...v0.7.1">https://github.com/zizmorcore/zizmor-action/compare/v0.7.0...v0.7.1</a></p>
<h2>v0.7.0</h2>
<p><a href="https://github.com/sponsors/woodruffw/">Sponsorship is appreciated!</a></p>
<h1>zizmor notes</h1>
<h2>Enhancements 🌱<a href="https://docs.zizmor.sh/release-notes/#enhancements">🔗</a></h2>
<ul>
<li>
<p>The <a href="https://docs.zizmor.sh/audits/#typosquat-uses">typosquat-uses</a> audit now supports pre-commit config inputs (<a href="https://redirect.github.com/zizmorcore/zizmor/issues/2437">#2437</a>)</p>
</li>
<li>
<p>The <a href="https://docs.zizmor.sh/audits/#self-hosted-runner">self-hosted-runner</a> audit is now significantly more precise (<a href="https://redirect.github.com/zizmorcore/zizmor/issues/2350">#2350</a>)</p>
<p>Many thanks to <a href="https://github.com/ubiratansoares"><code>@​ubiratansoares</code></a> for proposing and implementing this enhancement!</p>
</li>
<li>
<p>The <a href="https://docs.zizmor.sh/audits/#dangerous-triggers">dangerous-triggers</a> audit now considers the issue_comment trigger fundamentally dangerous (<a href="https://redirect.github.com/zizmorcore/zizmor/issues/2396">#2396</a>)</p>
</li>
<li>
<p>zizmor is now aware of the image_version workflow trigger (<a href="https://redirect.github.com/zizmorcore/zizmor/issues/2397">#2397</a>)</p>
</li>
<li>
<p>The <a href="https://docs.zizmor.sh/audits/#dangerous-triggers">dangerous-triggers</a> audit now produces more precise finding/diagnostic spans (<a href="https://redirect.github.com/zizmorcore/zizmor/issues/2402">#2402</a>)</p>
</li>
</ul>
<!-- raw HTML omitted -->
</blockquote>
<p>... (truncated)</p>
</details>
<details>
<summary>Commits</summary>
<ul>
<li><a href="https://github.com/zizmorcore/zizmor-action/commit/53290badfa369cb0bf7ad16342cb62c2fa47ac7b"><code>53290ba</code></a> Sync zizmor versions (<a href="https://redirect.github.com/zizmorcore/zizmor-action/issues/184">#184</a>)</li>
<li><a href="https://github.com/zizmorcore/zizmor-action/commit/76da2ee26867f8fbf24b824abef9e444636f76c3"><code>76da2ee</code></a> Support prereleases (<a href="https://redirect.github.com/zizmorcore/zizmor-action/issues/182">#182</a>)</li>
<li><a href="https://github.com/zizmorcore/zizmor-action/commit/fb1d87ba4b2f888025015a269a94ad7e991f6a7b"><code>fb1d87b</code></a> chore(deps): bump the github-actions group with 2 updates (<a href="https://redirect.github.com/zizmorcore/zizmor-action/issues/180">#180</a>)</li>
<li><a href="https://github.com/zizmorcore/zizmor-action/commit/be3660124653b2533ddba057e8651888379e64e2"><code>be36601</code></a> README: bump pins (<a href="https://redirect.github.com/zizmorcore/zizmor-action/issues/179">#179</a>)</li>
<li><a href="https://github.com/zizmorcore/zizmor-action/commit/85b2b9fb8e3325e7a23d7d0a9d7ba5ae0f999e3e"><code>85b2b9f</code></a> Sync zizmor versions (<a href="https://redirect.github.com/zizmorcore/zizmor-action/issues/178">#178</a>)</li>
<li><a href="https://github.com/zizmorcore/zizmor-action/commit/38c745a43764dcc8bcfd364e8295870e252e957c"><code>38c745a</code></a> README: bump pins (<a href="https://redirect.github.com/zizmorcore/zizmor-action/issues/177">#177</a>)</li>
<li><a href="https://github.com/zizmorcore/zizmor-action/commit/f9597c869b2e67095f9b527070297a13a6efe928"><code>f9597c8</code></a> Sync zizmor versions (<a href="https://redirect.github.com/zizmorcore/zizmor-action/issues/175">#175</a>)</li>
<li><a href="https://github.com/zizmorcore/zizmor-action/commit/2181e51ed8d58b9304e4a561374bb36d5599af3a"><code>2181e51</code></a> Bump sync-zizmor-versions.py to Python 3.11 for locking (<a href="https://redirect.github.com/zizmorcore/zizmor-action/issues/174">#174</a>)</li>
<li><a href="https://github.com/zizmorcore/zizmor-action/commit/27d989be9329e6cff7f80baf450ca83bf4c94898"><code>27d989b</code></a> chore(deps): bump the github-actions group with 2 updates (<a href="https://redirect.github.com/zizmorcore/zizmor-action/issues/173">#173</a>)</li>
<li><a href="https://github.com/zizmorcore/zizmor-action/commit/a91218c45134530ecc7aefc3eec3f997a279ad37"><code>a91218c</code></a> Bootstrap zizmor without Docker (<a href="https://redirect.github.com/zizmorcore/zizmor-action/issues/169">#169</a>)</li>
<li>Additional commits viewable in <a href="https://github.com/zizmorcore/zizmor-action/compare/70fb788f84895a7701f5643d103d587e460b5c99...53290badfa369cb0bf7ad16342cb62c2fa47ac7b">compare view</a></li>
</ul>
</details>
<br />


[![Dependabot compatibility score](https://dependabot-badges.githubapp.com/badges/compatibility_score?dependency-name=zizmorcore/zizmor-action&package-manager=github_actions&previous-version=0.6.3&new-version=0.8.0)](https://docs.github.com/en/github/managing-security-vulnerabilities/about-dependabot-security-updates#about-compatibility-scores)

Dependabot will resolve any conflicts with this PR as long as you don't alter it yourself. You can also trigger a rebase manually by commenting `@dependabot rebase`.

[//]: # (dependabot-automerge-start)
[//]: # (dependabot-automerge-end)

---

<details>
<summary>Dependabot commands and options</summary>
<br />

You can trigger Dependabot actions by commenting on this PR:
- `@dependabot rebase` will rebase this PR
- `@dependabot recreate` will recreate this PR, overwriting any edits that have been made to it
- `@dependabot show <dependency name> ignore conditions` will show all of the ignore conditions of the specified dependency
- `@dependabot ignore this major version` will close this PR and stop Dependabot creating any more for this major version (unless you reopen the PR or upgrade to it yourself)
- `@dependabot ignore this minor version` will close this PR and stop Dependabot creating any more for this minor version (unless you reopen the PR or upgrade to it yourself)
- `@dependabot ignore this dependency` will close this PR and stop Dependabot creating any more for this dependency (unless you reopen the PR or upgrade to it yourself)


</details>

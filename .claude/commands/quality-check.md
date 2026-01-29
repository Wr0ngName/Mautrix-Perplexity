Run comprehensive quality checks on the codebase.

## What this does

1. **Run all tests**
   - Backend: `php artisan test`
   - Frontend: `npm run test`

2. **Static analysis**
   - PHPStan: `./vendor/bin/phpstan analyse`
   - TypeScript: `npm run type-check`

3. **Code style**
   - Pint: `./vendor/bin/pint --test`
   - ESLint: `npm run lint`

4. **Use reviewer agent** to analyze recent changes

5. **Generate quality report**
   - Test results
   - Static analysis results
   - Code style violations
   - Manual review findings

## When to use

- Before committing code
- Before creating pull requests
- After implementing a feature
- As part of CI/CD validation

## Output

- ✅ All checks passed → Ready to commit
- ⚠️ Warnings found → Review suggested
- ❌ Critical issues → Must fix before commit

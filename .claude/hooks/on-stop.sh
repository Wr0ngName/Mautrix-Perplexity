#!/bin/bash

# This hook runs after Claude finishes responding
# It performs automated quality checks

echo "🔍 Running automated quality checks..."

# Check if we're in a Laravel project
if [ ! -f "artisan" ]; then
    echo "⚠️  Not in Laravel project root, skipping checks"
    exit 0
fi

# Initialize counters
ERRORS=0
WARNINGS=0

# Backend checks
if [ -f "vendor/bin/pint" ]; then
    echo ""
    echo "📝 Checking PHP code style..."
    if ! ./vendor/bin/pint --test > /tmp/pint.log 2>&1; then
        WARNINGS=$((WARNINGS + 1))
        echo "⚠️  Code style issues found (run ./vendor/bin/pint to fix)"
    else
        echo "✅ Code style OK"
    fi
fi

# TypeScript check
if [ -f "package.json" ] && grep -q "type-check" package.json; then
    echo ""
    echo "📝 Checking TypeScript..."
    if ! npm run type-check > /tmp/ts-check.log 2>&1; then
        TS_ERRORS=$(grep -c "error TS" /tmp/ts-check.log || echo "0")
        if [ "$TS_ERRORS" -gt 0 ]; then
            ERRORS=$((ERRORS + TS_ERRORS))
            echo "❌ TypeScript errors: $TS_ERRORS"
            if [ "$TS_ERRORS" -lt 5 ]; then
                echo ""
                grep "error TS" /tmp/ts-check.log | head -5
            fi
        fi
    else
        echo "✅ TypeScript OK"
    fi
fi

# Run tests (if there are any modified test files)
if git diff --name-only HEAD 2>/dev/null | grep -q "test"; then
    echo ""
    echo "🧪 Running tests..."
    
    # Backend tests
    if php artisan test --compact > /tmp/test.log 2>&1; then
        echo "✅ Backend tests pass"
    else
        ERRORS=$((ERRORS + 1))
        echo "❌ Backend tests failing"
        tail -20 /tmp/test.log
    fi
fi

# Summary
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
if [ $ERRORS -eq 0 ] && [ $WARNINGS -eq 0 ]; then
    echo "✅ All quality checks passed!"
elif [ $ERRORS -eq 0 ]; then
    echo "⚠️  Warnings: $WARNINGS (should fix)"
    echo "   Run 'npm run lint:fix' and './vendor/bin/pint' to auto-fix"
else
    echo "❌ Critical issues found: $ERRORS errors, $WARNINGS warnings"
    echo "   Please fix before committing"
fi
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

exit 0  # Don't block Claude, just report
